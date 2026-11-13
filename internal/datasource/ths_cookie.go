package datasource

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// 登录态 cookie 的校验与落盘（含本机浏览器只读提取）。
//
// 纪律：
//  1. 先验证再落盘 —— 拿用户粘进来的 cookie 真拉一次网页端自选，拉得到才写文件；
//     校验不过一律不写，避免用户把「保存成功」和「同步失败」当成两个独立问题。
//  2. 落盘是 <DataDir>/ths_cookie.txt，权限 0600，路径必须过 Guard（不能写到数据目录之外）。
//  3. 存储形态目前是「明文 + 0600」：与既有的 GUANLAN_THS_COOKIE 环境变量、手工粘贴路径
//     保持一致，不引入钥匙串依赖。**后续如需加固，可换 macOS 钥匙串（Security.framework /
//     keychain item）或至少加一层本地密钥；改动面只有写入处（SaveTHSCookie）与读取处
//     （thsCookie）两处，插件/前端接口不必变。**
//  4. 任何返回值与诊断文案都不得回显 cookie 内容（只允许出现文件路径与来源），
//     日志同理；Cookie 只在内存里传给上游请求头。
const (
	thsCookieMaxBytes = 16 << 10
)

// sanitizeCookie 清洗用户粘贴的 Cookie：去掉 "Cookie:" 前缀、换行与首尾空白。
// 用户从开发者工具复制时经常带上整行头名或多行，直接落盘会导致请求头非法。
func sanitizeCookie(raw string) string {
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(text), "cookie:") {
		text = strings.TrimSpace(text[len("cookie:"):])
	}
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\n", "")
	text = strings.ReplaceAll(text, "\t", "")
	return strings.TrimRight(strings.TrimSpace(text), ";")
}

// VerifyTHSCookie 用给定 cookie 真实请求一次网页端自选，返回能读到的标的数量。
// 空 cookie、过期 cookie、网络失败分别给不同的中文原因，前端可直接展示。
func (s *Service) VerifyTHSCookie(ctx context.Context, cookie string) (int, error) {
	cleaned := sanitizeCookie(cookie)
	if cleaned == "" {
		return 0, errors.New("Cookie 不能为空：请在浏览器登录同花顺网页版后，把开发者工具里请求头的 Cookie 整行复制过来")
	}
	if len(cleaned) > thsCookieMaxBytes {
		return 0, fmt.Errorf("Cookie 过长（%d 字节，上限 %d）：只需要 t.10jqka.com.cn 的登录态，不要整份浏览器导出", len(cleaned), thsCookieMaxBytes)
	}
	body, err := s.client.get(ctx, request{
		source:  sourceTHS,
		url:     thsWebAPIURL,
		referer: thsWebReferer,
		headers: map[string]string{
			"Cookie":           cleaned,
			"Accept":           "application/json, text/javascript, */*; q=0.01",
			"X-Requested-With": "XMLHttpRequest",
		},
	})
	if err != nil {
		return 0, fmt.Errorf("校验登录态失败（网络或同花顺服务异常）: %w", err)
	}
	group, err := parseTHSWebSelfStock(body)
	if err != nil {
		if errors.Is(err, ErrNeedWebLogin) {
			s.markWebLoginInvalid(err.Error())
			return 0, fmt.Errorf("登录态无效（cookie 已过期或复制不完整）：%w", err)
		}
		return 0, fmt.Errorf("登录态校验未通过：%w", err)
	}
	// 新 cookie 被服务端接受，之前的「已失效」标记必须清掉，否则诊断会一直报警。
	s.clearWebLoginInvalid()
	return len(group.Codes), nil
}

// SaveTHSCookie 校验通过后写 <DataDir>/ths_cookie.txt（0600）。
// 校验不过就不落盘 —— 否则用户会把「保存成功」和「同步失败」当成两个独立 bug 报上来。
func (s *Service) SaveTHSCookie(ctx context.Context, cookie string) (int, error) {
	count, err := s.VerifyTHSCookie(ctx, cookie)
	if err != nil {
		return 0, err
	}
	path := s.thsCookiePath()
	if path == "" {
		return 0, errors.New("未配置数据目录，无法保存登录态")
	}
	if s.cfg.Guard != nil {
		if err := s.cfg.Guard.CheckPath(path); err != nil {
			return 0, fmt.Errorf("登录态文件路径不在允许目录内: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, fmt.Errorf("创建数据目录失败: %w", err)
	}
	if err := os.WriteFile(path, []byte(sanitizeCookie(cookie)+"\n"), 0o600); err != nil {
		return 0, fmt.Errorf("保存登录态失败: %w", err)
	}
	// 已存在的文件写入时不会改权限，这里显式收紧到 0600。
	if err := os.Chmod(path, 0o600); err != nil {
		return 0, fmt.Errorf("设置登录态文件权限失败: %w", err)
	}
	return count, nil
}

// 浏览器 cookie 提取（尽力而为，只读）。
//
// Safari 的 Cookies.binarycookies 是未加密的，可以只读解析；Chrome/Edge/Chromium 的
// cookie 值用钥匙串里的密钥做了 AES 加密，本应用既不解密也不去调 security 命令行
// （安全红线：不新增 exec 调用），而是明确告诉用户「请粘贴」。
const (
	safariCookieRel       = "Library/Containers/com.apple.Safari/Data/Library/Cookies/Cookies.binarycookies"
	safariCookieLegacyRel = "Library/Cookies/Cookies.binarycookies"
	thsCookieDomain       = "10jqka.com.cn"
)

// chromiumCookieRels 只用来判断「装了 Chromium 系浏览器」，不做读取。
var chromiumCookieRels = []string{
	"Library/Application Support/Google/Chrome/Default/Cookies",
	"Library/Application Support/Microsoft Edge/Default/Cookies",
	"Library/Application Support/Chromium/Default/Cookies",
}

// browserCookie 是解析出来的一个 cookie（只保留拼请求头需要的字段）。
type browserCookie struct {
	Domain string
	Name   string
	Path   string
	Value  string
}

// CaptureBrowserTHSCookie 尽力从本机浏览器读同花顺登录态：
//   - Safari：直接解析 binarycookies（只读，不启动任何进程）；
//   - Chromium 系：值被钥匙串加密，明确返回「请粘贴」，绝不返回空成功。
func (s *Service) CaptureBrowserTHSCookie() (string, string, error) {
	if s.home == "" {
		return "", "", errors.New("无法定位用户主目录，读不到浏览器 cookie")
	}
	reasons := make([]string, 0, 3)
	for _, rel := range []string{safariCookieRel, safariCookieLegacyRel} {
		path := filepath.Join(s.home, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			reasons = append(reasons, fmt.Sprintf("读取 Safari cookie 失败（%v）", err))
			continue
		}
		cookies, parseErr := parseBinaryCookies(raw)
		if parseErr != nil {
			reasons = append(reasons, "Safari cookie 解析失败："+parseErr.Error())
			continue
		}
		if header := cookieHeaderFor(cookies, thsCookieDomain); header != "" {
			return header, "safari", nil
		}
		reasons = append(reasons, "Safari 里没有 "+thsCookieDomain+" 的 cookie（先在 Safari 登录同花顺网页版再回来）")
	}
	if dbs, err := detectChromiumCookieStore(s.home); err == nil {
		return "", "", fmt.Errorf("检测到 Chromium 系浏览器 cookie 库（%s，共 %d 个），但它的值是钥匙串加密的，本应用不做解密：请打开同花顺网页版，复制请求头里的 Cookie，点「粘贴登录态」保存", dbs[0], len(dbs))
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "本机没有找到 Safari 的 Cookies.binarycookies")
	}
	return "", "", errors.New(strings.Join(reasons, "；") + "。请登录同花顺网页版后手动粘贴 Cookie")
}

// cookieHeaderFor 把域名匹配的 cookie 拼成请求头形态（name=value; name=value）。
func cookieHeaderFor(cookies []browserCookie, domainSuffix string) string {
	parts := make([]string, 0, len(cookies))
	seen := make(map[string]bool, len(cookies))
	for _, cookie := range cookies {
		domain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(cookie.Domain)), ".")
		if domain != domainSuffix && !strings.HasSuffix(domain, "."+domainSuffix) {
			continue
		}
		if cookie.Name == "" || seen[cookie.Name] {
			continue
		}
		seen[cookie.Name] = true
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

// binaryCookieHeaderLen 是单条 cookie 记录的固定头长度（实测 56 字节，之后是字符串区）。
const binaryCookieHeaderLen = 56

// parseBinaryCookies 解析 Safari 的 Cookies.binarycookies。
//
// 文件头是「大端」（magic cook + 页数 + 每页长度），而页内计数/偏移/记录字段是「小端」，
// 这是实机验证出来的坑：按大端读页内整数会得到 16777216 这种荒谬的 cookie 数。
func parseBinaryCookies(raw []byte) ([]browserCookie, error) {
	if len(raw) < 8 {
		return nil, errors.New("cookie 文件太短，不是有效的 Cookies.binarycookies")
	}
	if !bytes.HasPrefix(raw, []byte("cook")) {
		return nil, errors.New("缺少 cook 文件头，不是 Safari 的 Cookies.binarycookies")
	}
	pageCount := int(binary.BigEndian.Uint32(raw[4:8]))
	if pageCount <= 0 || pageCount > 4096 {
		return nil, fmt.Errorf("cookie 文件页数异常（%d）", pageCount)
	}
	headerLen := 8 + 4*pageCount
	if len(raw) < headerLen {
		return nil, errors.New("cookie 文件被截断（页表不完整）")
	}
	out := make([]browserCookie, 0, 16)
	offset := headerLen
	for i := 0; i < pageCount; i++ {
		size := int(binary.BigEndian.Uint32(raw[8+4*i : 12+4*i]))
		if size <= 0 || offset+size > len(raw) {
			// 截断的文件：能读多少读多少，不因为尾部破损丢掉前面的可用 cookie。
			break
		}
		out = append(out, parseBinaryCookiePage(raw[offset:offset+size])...)
		offset += size
	}
	return out, nil
}

// parseBinaryCookiePage 解析一页 cookie。页内任何一条坏记录只跳过该条，不影响整页。
func parseBinaryCookiePage(page []byte) []browserCookie {
	if len(page) < 8 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(page[4:8]))
	if count <= 0 || count > 4096 || 8+4*count > len(page) {
		return nil
	}
	out := make([]browserCookie, 0, count)
	for i := 0; i < count; i++ {
		recordOffset := int(binary.LittleEndian.Uint32(page[8+4*i : 12+4*i]))
		if recordOffset <= 0 || recordOffset+binaryCookieHeaderLen > len(page) {
			continue
		}
		record := page[recordOffset:]
		if size := int(binary.LittleEndian.Uint32(record[0:4])); size > 0 && size < len(record) {
			record = record[:size]
		}
		if len(record) < binaryCookieHeaderLen {
			continue
		}
		readString := func(fieldOffset int) string {
			at := int(binary.LittleEndian.Uint32(record[fieldOffset : fieldOffset+4]))
			if at <= 0 || at >= len(record) {
				return ""
			}
			rest := record[at:]
			if end := bytes.IndexByte(rest, 0); end >= 0 {
				rest = rest[:end]
			}
			return string(rest)
		}
		cookie := browserCookie{
			Domain: readString(16),
			Name:   readString(20),
			Path:   readString(24),
			Value:  readString(28),
		}
		if cookie.Name == "" || cookie.Domain == "" {
			continue
		}
		out = append(out, cookie)
	}
	return out
}
