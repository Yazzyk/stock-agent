package datasource

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 同花顺 macOS 客户端把「自选分组」放在沙盒容器：
//
//	~/Library/Containers/cn.com.10jqka.macstock/Data/Documents/cloud_store/blockstock_<uid>/public/*.dat
//
// 每个 .dat 是一个 NSKeyedArchiver 归档（类名 SSGLocalSaveEntityFile），其中
//
//	self.ID    = 分组 ID 的 protobuf varint（0 表示「分组排序」归档）
//	self.value = 分组内容的 protobuf：
//	             field1 = base64(GBK 分组名)
//	             field3 = "600000|000001|,17|33|"（前半段是 6 位代码，逗号后是同花顺市场码）
//
// 全程只读，不修改同花顺任何文件；路径被 TCC 拒绝时返回 ErrNeedFullDiskAccess，
// 绝不能把「没权限」伪装成「没装客户端」。
const (
	thsContainerRel  = "Library/Containers/cn.com.10jqka.macstock/Data"
	thsCloudStoreRel = "Documents/cloud_store"
	thsUserIDKey     = "dXNlcmlk" // base64("userid")
	thsOrderGroupID  = 0
	thsDatSuffix     = ".dat"
	thsContainerTag  = "__public__"
)

var (
	// ErrTHSNotInstalled 表示本机没有同花顺客户端容器目录（或非 macOS）。
	ErrTHSNotInstalled = errors.New("未找到同花顺客户端本地自选数据（自选分组同步仅支持 macOS 版同花顺客户端）")
	// ErrNeedFullDiskAccess 表示容器存在但被 macOS 隐私保护拦住。
	ErrNeedFullDiskAccess = errors.New("没有读取同花顺本地数据的权限：请到「系统设置 → 隐私与安全性 → 完全磁盘访问权限」勾选本应用后重启")
	// ErrTHSNoGroup 表示目录可读但没有分组归档（未登录或从未建组）。
	ErrTHSNoGroup = errors.New("同花顺本地数据目录可读，但没有自选分组：请先在同花顺客户端登录并创建自选分组")
)

// thsGroup 是一个同花顺自选分组（只读解析结果）。
type thsGroup struct {
	ExternalID string
	Name       string
	Codes      []string // 应用内代码：sh600000 / sz000530 / bj830799
}

// scanTHSLocal 定位并解析本机同花顺容器里的自选分组。home 由调用方给出，便于测试注入。
func scanTHSLocal(home string) ([]thsGroup, string, error) {
	container := filepath.Join(home, thsContainerRel)
	if _, err := os.ReadDir(container); err != nil {
		return nil, "", classifyTHSErr(err)
	}
	cloudStore := filepath.Join(container, thsCloudStoreRel)
	entries, err := os.ReadDir(cloudStore)
	if err != nil {
		return nil, "", classifyTHSErr(err)
	}
	bestDir, bestCount := "", -1
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "blockstock_") {
			continue
		}
		dir := filepath.Join(cloudStore, entry.Name())
		files, collectErr := collectTHSDats(dir)
		if collectErr != nil {
			if errors.Is(collectErr, ErrNeedFullDiskAccess) {
				return nil, "", collectErr
			}
			continue
		}
		if len(files) > bestCount {
			bestDir, bestCount = dir, len(files)
		}
	}
	if bestDir == "" || bestCount <= 0 {
		return nil, "", ErrTHSNoGroup
	}
	groups, userID, err := parseTHSDir(bestDir)
	if err != nil {
		return nil, "", err
	}
	if userID == "" {
		userID = thsUserIDFromDir(bestDir)
	}
	return groups, userID, nil
}

// classifyTHSErr 区分「没装」与「没权限」。
func classifyTHSErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrPermission) {
		return ErrNeedFullDiskAccess
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && errors.Is(pathErr.Err, fs.ErrPermission) {
		return ErrNeedFullDiskAccess
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ErrTHSNotInstalled
	}
	return ErrTHSNotInstalled
}

// collectTHSDats 收集目录下的 .dat 归档：先看目录本身与 public 子目录，再退化为逐层遍历。
// 必须用 os.ReadDir（而不是 filepath.Glob）：Glob 会把 EPERM 当成「没有匹配」，
// 让权限问题伪装成「没数据」。
func collectTHSDats(dir string) ([]string, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, classifyTHSErr(err)
	}
	read := func(target string) ([]string, error) {
		entries, err := os.ReadDir(target)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil, ErrNeedFullDiskAccess
			}
			return nil, nil
		}
		matched := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), thsDatSuffix) {
				continue
			}
			matched = append(matched, filepath.Join(target, entry.Name()))
		}
		return matched, nil
	}
	for _, target := range []string{dir, filepath.Join(dir, "public")} {
		matched, err := read(target)
		if err != nil {
			return nil, err
		}
		if len(matched) > 0 {
			return matched, nil
		}
	}
	var walked []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return ErrNeedFullDiskAccess
			}
			return nil
		}
		if entry == nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), thsDatSuffix) {
			return nil
		}
		walked = append(walked, path)
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNeedFullDiskAccess) {
			return nil, err
		}
		return nil, ErrTHSNoGroup
	}
	return walked, nil
}

// parseTHSDir 解析目录下全部 .dat 归档，返回分组与 userid（userid 只是本地标识，不含凭证）。
func parseTHSDir(dir string) ([]thsGroup, string, error) {
	files, err := collectTHSDats(dir)
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", ErrTHSNoGroup
	}
	groups := make([]thsGroup, 0, len(files))
	var order []int
	userID := ""
	skipped := 0
	for _, path := range files {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		entity, decodeErr := decodeKeyedArchive(raw)
		if decodeErr != nil || entity == nil {
			continue
		}
		payload, _ := entity["self.value"].([]byte)
		idBytes, _ := entity["self.ID"].([]byte)
		key := thsArchiveKey(path)
		if key == thsUserIDKey && len(payload) > 0 {
			userID = strings.TrimSpace(string(payload))
			continue
		}
		groupID := thsGroupIDFromIDBytes(idBytes)
		if groupID < 0 {
			groupID = thsGroupIDFromFileKey(key)
		}
		if groupID == thsOrderGroupID {
			order = parseTHSGroupOrder(payload)
			continue
		}
		if groupID < 0 || len(payload) == 0 {
			continue
		}
		group, ok := parseTHSGroupPayload(groupID, payload)
		if !ok {
			skipped++
			continue
		}
		groups = append(groups, group)
	}
	sortTHSGroups(groups, order)
	if len(groups) == 0 {
		if skipped > 0 {
			return nil, userID, ErrTHSNoGroup
		}
		return nil, userID, ErrTHSNoGroup
	}
	return groups, userID, nil
}

// parseTHSGroupPayload 把一个分组的 protobuf 载荷解析成 thsGroup；没有成分股时 ok=false
// （问财条件选股这类分组无法映射成自选列表）。
func parseTHSGroupPayload(groupID int, payload []byte) (thsGroup, bool) {
	group := thsGroup{ExternalID: strconv.Itoa(groupID)}
	codes := make([]string, 0, 16)
	seen := make(map[string]bool, 16)
	var names []string
	for _, field := range parseProtoFields(payload) {
		if field.WireType != protoWireBytes || len(field.Bytes) == 0 {
			continue
		}
		switch field.Number {
		case 1, 2:
			if text := decodeTHSBase64Text(field.Bytes); text != "" {
				names = append(names, text)
			}
		case 3:
			for _, raw := range splitTHSCodeString(string(field.Bytes)) {
				code, ok := thsCodeToCode(raw)
				if !ok || seen[code.Symbol] {
					continue
				}
				seen[code.Symbol] = true
				codes = append(codes, code.MarketSymbol())
			}
		}
	}
	if len(codes) == 0 {
		return group, false
	}
	group.Codes = codes
	for _, name := range names {
		if name != "" {
			group.Name = name
			break
		}
	}
	if group.Name == "" {
		group.Name = "分组" + group.ExternalID
	}
	return group, true
}

// thsCodeToCode 把同花顺 6 位代码转成应用内代码，跳过板块/概念指数（881~885 无个股行情）。
func thsCodeToCode(raw string) (Code, bool) {
	text := strings.TrimSpace(raw)
	if len(text) != 6 {
		return Code{}, false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return Code{}, false
		}
	}
	if strings.HasPrefix(text, "881") || strings.HasPrefix(text, "883") ||
		strings.HasPrefix(text, "884") || strings.HasPrefix(text, "885") {
		return Code{}, false
	}
	code, err := ParseCode(text)
	if err != nil {
		return Code{}, false
	}
	return code, true
}

// splitTHSCodeString 拆分 "600000|000001|,17|33|"：只有 6 位数字段是代码，
// 逗号后的市场码（17/33）不直接使用——按代码前缀推断更可靠，也避免 9 开头 B 股被误判。
func splitTHSCodeString(text string) []string {
	out := make([]string, 0, 16)
	for _, token := range strings.Split(text, "|") {
		token = strings.TrimSpace(token)
		token = strings.TrimPrefix(token, ",")
		if len(token) != 6 {
			continue
		}
		digitsOnly := true
		for _, r := range token {
			if r < '0' || r > '9' {
				digitsOnly = false
				break
			}
		}
		if digitsOnly {
			out = append(out, token)
		}
	}
	return out
}

// decodeTHSBase64Text 解 base64(GBK) 文本。先按 GBK 解，只有出现替换字符才回退 UTF-8，
// 否则像「医药」（GBK 字节恰好也是合法 UTF-8）会被解成乱码。
func decodeTHSBase64Text(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) < 4 {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(padBase64(text))
	if err != nil || len(decoded) == 0 {
		return ""
	}
	if gbk, gbkErr := simplifiedchinese.GBK.NewDecoder().Bytes(decoded); gbkErr == nil {
		candidate := strings.TrimSpace(string(gbk))
		if candidate != "" && !strings.ContainsRune(candidate, '\uFFFD') {
			return candidate
		}
	}
	if utf8Valid(decoded) {
		return strings.TrimSpace(string(decoded))
	}
	return ""
}

func utf8Valid(data []byte) bool { return utf8.Valid(data) }

func padBase64(text string) string {
	if remains := len(text) % 4; remains != 0 {
		return text + strings.Repeat("=", 4-remains)
	}
	return text
}

// thsArchiveKey 取归档文件名里的 base64 键：blockstock_631110168__public__CCk=.dat → CCk=
func thsArchiveKey(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), thsDatSuffix)
	if idx := strings.LastIndex(base, thsContainerTag); idx >= 0 {
		return base[idx+len(thsContainerTag):]
	}
	return base
}

// thsGroupIDFromIDBytes 从 self.ID 的 protobuf field1 取分组 ID。
func thsGroupIDFromIDBytes(raw []byte) int {
	for _, field := range parseProtoFields(raw) {
		if field.Number == 1 && field.WireType == protoWireVarint {
			return int(field.Varint)
		}
	}
	return -1
}

// thsGroupIDFromFileKey 用文件名里的 base64 键兜底取分组 ID。
func thsGroupIDFromFileKey(key string) int {
	decoded, err := base64.StdEncoding.DecodeString(padBase64(key))
	if err != nil || len(decoded) == 0 {
		return -1
	}
	return thsGroupIDFromIDBytes(decoded)
}

// thsUserIDFromDir 从 blockstock_<userid> 目录名取 userid。
func thsUserIDFromDir(dir string) string {
	for _, part := range strings.Split(filepath.Clean(dir), string(os.PathSeparator)) {
		if strings.HasPrefix(part, "blockstock_") {
			return strings.TrimPrefix(part, "blockstock_")
		}
	}
	return ""
}

// parseTHSGroupOrder 解析排序归档：payload.field1 = "p29,14B,146,..."（十六进制分组 ID）。
func parseTHSGroupOrder(payload []byte) []int {
	for _, field := range parseProtoFields(payload) {
		if field.Number != 1 || field.WireType != protoWireBytes {
			continue
		}
		ids := make([]int, 0, 32)
		for i, token := range strings.Split(string(field.Bytes), ",") {
			token = strings.TrimSpace(token)
			if i == 0 {
				token = strings.TrimLeftFunc(token, func(r rune) bool {
					return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'))
				})
			}
			if token == "" {
				continue
			}
			value, err := strconv.ParseInt(token, 16, 32)
			if err != nil || value <= 0 {
				continue
			}
			ids = append(ids, int(value))
		}
		if len(ids) > 0 {
			return ids
		}
	}
	return nil
}

// sortTHSGroups 按客户端里的分组顺序排序，不在顺序表里的按 ID 升序排后面。
func sortTHSGroups(groups []thsGroup, order []int) {
	position := make(map[int]int, len(order))
	for i, id := range order {
		position[id] = i
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, leftErr := strconv.Atoi(groups[i].ExternalID)
		right, rightErr := strconv.Atoi(groups[j].ExternalID)
		leftPos, leftOK := position[left]
		rightPos, rightOK := position[right]
		switch {
		case leftOK && rightOK:
			return leftPos < rightPos
		case leftOK:
			return true
		case rightOK:
			return false
		case leftErr == nil && rightErr == nil:
			return left < right
		}
		return groups[i].ExternalID < groups[j].ExternalID
	})
}
