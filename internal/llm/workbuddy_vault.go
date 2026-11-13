package llm

// WorkBuddy 本地凭证库。
//
// 落盘结构与 dsh / go-stock 的 workbuddy-vault 同构，两个方向都能直接拷：
// <root>/<cn|global>/<accountId>.json，权限 0600，临时文件 + rename 原子写。
// 读取优先级：本地库 → dsh（$DSH_HOME / ~/.dsh）→ go-stock（其 DataDir 候选路径）。
// dsh / go-stock 只作为导入源：一旦本地有过登录或导入，运行时就不再依赖它们。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// workBuddyVaultDirName 本地凭证库目录名，挂在应用数据目录下。
	workBuddyVaultDirName = "workbuddy-vault"
	// workBuddyRegionCN / workBuddyRegionGlobal 与 dsh 的区域目录名保持一致。
	workBuddyRegionCN     = "cn"
	workBuddyRegionGlobal = "global"
	// 凭证来源标记：本地库、dsh、go-stock。
	workBuddyCredSourceLocal   = "local"
	workBuddyCredSourceDSH     = "dsh"
	workBuddyCredSourceGoStock = "go-stock"
)

// errWorkBuddyNotLoggedIn 本机没有任何可用的 WorkBuddy 登录态。
var errWorkBuddyNotLoggedIn = errors.New("未找到 WorkBuddy 登录凭证：请在「模型设置 → WorkBuddy」里登录一次，或从 dsh / go-stock 导入")

// workBuddyCredential 是内存态的登录凭证，绝不外传。
type workBuddyCredential struct {
	AccessToken  string
	RefreshToken string
	Domain       string // 如 www.workbuddy.cn，决定请求走国内还是国际网关
	ExpiresAt    int64  // accessToken 到期（秒）
	RefreshAt    int64  // refreshToken 到期（秒）
	LastRefresh  int64  // 上次续期（秒）
	AccountID    string
	UID          string
	AccountName  string
	FilePath     string
	Source       string
}

// workBuddyVaultDoc 是凭证落盘结构（与 dsh / go-stock 同构）。
type workBuddyVaultDoc struct {
	Version       int    `json:"version"`
	AccountID     string `json:"accountId"`
	RefreshedAtMs int64  `json:"refreshedAtMs"`
	Auth          struct {
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		ExpiresAt        int64  `json:"expiresAt"`
		RefreshExpiresAt int64  `json:"refreshExpiresAt"`
		LastRefreshTime  int64  `json:"lastRefreshTime"`
		Domain           string `json:"domain"`
	} `json:"auth"`
	Account struct {
		UID      string `json:"uid"`
		UIN      string `json:"uin"`
		Nickname string `json:"nickname"`
	} `json:"account"`
}

// workBuddyVaultCandidate 是一份候选凭证及其来源。
type workBuddyVaultCandidate struct {
	cred   *workBuddyCredential
	path   string
	source string
}

// workBuddyExternalRoot 是一个外部凭证库根目录及其来源。
type workBuddyExternalRoot struct {
	root   string
	source string
}

// workBuddyRegionOf 由 domain 推区域目录名，未知域名按国际版处理。
func workBuddyRegionOf(domain string) string {
	lower := strings.ToLower(strings.TrimSpace(domain))
	if strings.HasSuffix(lower, "workbuddy.cn") || strings.HasSuffix(lower, "codebuddy.cn") ||
		lower == "cn" || lower == "codebuddy" {
		return workBuddyRegionCN
	}
	return workBuddyRegionGlobal
}

// normalizeWorkBuddyTimestamp 把秒/毫秒混用的时间戳统一成秒。
func normalizeWorkBuddyTimestamp(value int64) int64 {
	if value <= 0 {
		return 0
	}
	if value > 1_000_000_000_000 {
		return value / 1000
	}
	return value
}

// sanitizeWorkBuddyFileName 清掉文件名不安全字符。
func sanitizeWorkBuddyFileName(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_", "..", "_")
	return replacer.Replace(strings.TrimSpace(value))
}

// workBuddyShortHash 返回 sha256 前 16 个 hex（无账号 ID 时用于文件名与缓存指纹）。
func workBuddyShortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

// workBuddyVaultFileName 决定凭证文件名：优先账号 ID，其次 UID，最后 token 指纹。
func workBuddyVaultFileName(cred *workBuddyCredential) string {
	if cred == nil {
		return ""
	}
	if id := strings.TrimSpace(cred.AccountID); id != "" {
		return sanitizeWorkBuddyFileName(id) + ".json"
	}
	if uid := strings.TrimSpace(cred.UID); uid != "" {
		return "uid_" + sanitizeWorkBuddyFileName(uid) + ".json"
	}
	if cred.AccessToken == "" {
		return ""
	}
	return "token_" + workBuddyShortHash(cred.AccessToken) + ".json"
}

// readWorkBuddyFileNoFollow 读凭证文件，拒绝符号链接（避免凭证目录被指向奇怪位置）。
func readWorkBuddyFileNoFollow(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("拒绝读取符号链接凭证文件: %s", path)
	}
	return os.ReadFile(path)
}

// vaultRoot 返回本应用 WorkBuddy 凭证库根目录。
func (w *workBuddy) vaultRoot() string { return w.root }

// vaultPath 返回凭证库根目录（界面展示/备份用）。
func (w *workBuddy) vaultPath() string {
	if w == nil {
		return ""
	}
	return w.root
}

// parseVaultFile 解析单个凭证文件；缺 accessToken 视为无效。
func (w *workBuddy) parseVaultFile(path string) (*workBuddyCredential, error) {
	raw, err := readWorkBuddyFileNoFollow(path)
	if err != nil {
		return nil, err
	}
	var doc workBuddyVaultDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	accessToken := strings.TrimSpace(doc.Auth.AccessToken)
	if accessToken == "" {
		return nil, errWorkBuddyNotLoggedIn
	}
	return &workBuddyCredential{
		AccessToken:  accessToken,
		RefreshToken: strings.TrimSpace(doc.Auth.RefreshToken),
		Domain:       strings.TrimSpace(doc.Auth.Domain),
		ExpiresAt:    normalizeWorkBuddyTimestamp(doc.Auth.ExpiresAt),
		RefreshAt:    normalizeWorkBuddyTimestamp(doc.Auth.RefreshExpiresAt),
		LastRefresh:  normalizeWorkBuddyTimestamp(doc.Auth.LastRefreshTime),
		AccountID:    strings.TrimSpace(doc.AccountID),
		UID:          strings.TrimSpace(doc.Account.UID),
		AccountName:  strings.TrimSpace(doc.Account.Nickname),
		FilePath:     path,
	}, nil
}

// vaultDocOf 把内存凭证转成落盘结构。
func workBuddyVaultDocOf(cred *workBuddyCredential) workBuddyVaultDoc {
	doc := workBuddyVaultDoc{
		Version:       1,
		AccountID:     cred.AccountID,
		RefreshedAtMs: time.Now().UnixMilli(),
	}
	doc.Auth.AccessToken = cred.AccessToken
	doc.Auth.RefreshToken = cred.RefreshToken
	doc.Auth.ExpiresAt = cred.ExpiresAt
	doc.Auth.RefreshExpiresAt = cred.RefreshAt
	doc.Auth.Domain = cred.Domain
	doc.Auth.LastRefreshTime = cred.LastRefresh
	doc.Account.UID = cred.UID
	doc.Account.Nickname = cred.AccountName
	return doc
}

// saveCredential 把凭证写进本地库（原子写、0600），返回落盘路径。
func (w *workBuddy) saveCredential(cred *workBuddyCredential) (string, error) {
	if cred == nil || strings.TrimSpace(cred.AccessToken) == "" {
		return "", errors.New("WorkBuddy 凭证为空，无法保存")
	}
	dir := filepath.Join(w.root, workBuddyRegionOf(cred.Domain))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建 WorkBuddy 凭证目录失败: %w", err)
	}
	name := workBuddyVaultFileName(cred)
	if name == "" {
		return "", errors.New("无法确定 WorkBuddy 凭证文件名")
	}
	path := filepath.Join(dir, name)
	payload, err := json.MarshalIndent(workBuddyVaultDocOf(cred), "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".workbuddy-*.json.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err = tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return path, nil
}

// listVaultFiles 枚举某个根目录下的全部凭证文件，附带来源标记。
func (w *workBuddy) listVaultFiles(root, source string) []workBuddyVaultCandidate {
	regions, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]workBuddyVaultCandidate, 0, 4)
	for _, regionDir := range regions {
		if !regionDir.IsDir() {
			continue
		}
		dir := filepath.Join(root, regionDir.Name())
		entries, dirErr := os.ReadDir(dir)
		if dirErr != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			cred, parseErr := w.parseVaultFile(path)
			if parseErr != nil || cred == nil {
				continue
			}
			cred.Source = source
			out = append(out, workBuddyVaultCandidate{cred: cred, path: path, source: source})
		}
	}
	return out
}

// workBuddyDSHRoots 返回 dsh 可能存放 WorkBuddy 登录态的根目录（去重）。
func workBuddyDSHRoots() []string {
	roots := make([]string, 0, 2)
	seen := map[string]struct{}{}
	appendRoot := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	if home := strings.TrimSpace(os.Getenv("DSH_HOME")); home != "" {
		appendRoot(filepath.Join(home, workBuddyVaultDirName))
	}
	if userHome, err := os.UserHomeDir(); err == nil {
		appendRoot(filepath.Join(userHome, ".dsh", workBuddyVaultDirName))
	}
	return roots
}

// workBuddyGoStockRoots 按 go-stock apppath 的解析规则列出可能的凭证库目录。
// go-stock 的 DataDir = <BaseDir>/data，凭证库 = <DataDir>/workbuddy-vault。
// BaseDir 优先级：$GO_STOCK_ROOT_DIR → 工作目录（有 data/stock.db）→ 可执行文件目录 →
// macOS 的 ~/Library/Application Support/go-stock → 可执行文件目录 → ~/.config/go-stock。
// 这里取其中可静态判断的候选，并在读之前做存在性检查；源码仓库路径仅为开发机方便。
func workBuddyGoStockRoots() []string {
	bases := make([]string, 0, 4)
	if root := strings.TrimSpace(os.Getenv("GO_STOCK_ROOT_DIR")); root != "" {
		bases = append(bases, root)
	}
	if userHome, err := os.UserHomeDir(); err == nil {
		bases = append(bases,
			filepath.Join(userHome, "Library", "Application Support", "go-stock"),
			filepath.Join(userHome, ".config", "go-stock"),
			filepath.Join(userHome, "workspace", "go-stock"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	roots := make([]string, 0, len(bases))
	seen := map[string]struct{}{}
	for _, base := range bases {
		root := filepath.Join(base, "data", workBuddyVaultDirName)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		roots = append(roots, root)
	}
	return roots
}

// workBuddyExternalRoots 返回外部导入源（dsh 在前，go-stock 在后）。
func workBuddyExternalRoots() []workBuddyExternalRoot {
	out := make([]workBuddyExternalRoot, 0, 4)
	seen := map[string]struct{}{}
	for _, root := range workBuddyDSHRoots() {
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, workBuddyExternalRoot{root: root, source: workBuddyCredSourceDSH})
	}
	for _, root := range workBuddyGoStockRoots() {
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, workBuddyExternalRoot{root: root, source: workBuddyCredSourceGoStock})
	}
	return out
}

// externalRoots 返回外部导入源；测试可注入空列表以隔离本机真实凭证。
func (w *workBuddy) externalRoots() []workBuddyExternalRoot {
	if w.externalRootsOverride != nil {
		return w.externalRootsOverride()
	}
	return workBuddyExternalRoots()
}

// candidates 按优先级收集全部候选：本地库在前，dsh、go-stock 在后。
func (w *workBuddy) candidates() []workBuddyVaultCandidate {
	candidates := w.listVaultFiles(w.root, workBuddyCredSourceLocal)
	for _, ext := range w.externalRoots() {
		candidates = append(candidates, w.listVaultFiles(ext.root, ext.source)...)
	}
	return candidates
}

// bestCredentialFromVault 从本地库与外部导入源里挑一份仍可用的凭证。
// 同来源内取 accessToken 过期时间最晚的；取自外部源的凭证顺手迁进本地库。
func (w *workBuddy) bestCredentialFromVault() (*workBuddyCredential, error) {
	candidates := w.candidates()
	if len(candidates) == 0 {
		return nil, errWorkBuddyNotLoggedIn
	}
	priority := map[string]int{
		workBuddyCredSourceLocal:   0,
		workBuddyCredSourceDSH:     1,
		workBuddyCredSourceGoStock: 2,
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		pi, pj := priority[candidates[i].source], priority[candidates[j].source]
		if pi != pj {
			return pi < pj
		}
		return candidates[i].cred.ExpiresAt > candidates[j].cred.ExpiresAt
	})
	chosen := candidates[0]
	cred := chosen.cred
	cred.FilePath = chosen.path
	if chosen.source != workBuddyCredSourceLocal {
		if path, err := w.saveCredential(cred); err == nil {
			cred.Source = workBuddyCredSourceLocal
			cred.FilePath = path
		} else {
			w.log.Warn("WorkBuddy 凭证迁移到本地库失败", "source", chosen.source, "error", err.Error())
		}
	}
	return cred, nil
}

// purgeVault 清空本地凭证库（退出登录用）。
func (w *workBuddy) purgeVault() error {
	if _, err := os.Stat(w.root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(w.root)
}

// importExternalCredentials 把 dsh / go-stock 的凭证一次性拷进本地库，返回导入数量与账号名。
func (w *workBuddy) importExternalCredentials() (int, []string, error) {
	roots := w.externalRoots()
	if len(roots) == 0 {
		return 0, nil, errors.New("没有找到 dsh（$DSH_HOME 或 ~/.dsh/workbuddy-vault）或 go-stock 的 WorkBuddy 凭证库")
	}
	imported := 0
	names := make([]string, 0, len(roots))
	for _, ext := range roots {
		for _, candidate := range w.listVaultFiles(ext.root, ext.source) {
			if _, err := w.saveCredential(candidate.cred); err != nil {
				continue
			}
			imported++
			name := candidate.cred.AccountName
			if name == "" {
				name = candidate.cred.AccountID
			}
			if name == "" {
				name = "未命名账号"
			}
			names = append(names, name+"（"+ext.source+"）")
		}
	}
	if imported == 0 {
		return 0, nil, errors.New("dsh / go-stock 的凭证库存在但没读到可用凭证，或写入本地库失败")
	}
	return imported, names, nil
}
