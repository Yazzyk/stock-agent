package datasource

import (
	"context"
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestHTTPClient 把 fakeTransport 包成 http.Client，供不走 newServiceWithTransport 的用例。
func newTestHTTPClient(transport *fakeTransport) *http.Client {
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

// thsWebFixture 是网页端自选接口的最小成功响应（结构同 parseTHSWebSelfStock 的期望）。
// 真实响应需要登录态，这里只造结构，字段名与线上一致（errorCode/result/code/marketid）。
const thsWebFixture = `{"errorCode":0,"errorMsg":"","result":[{"code":"600000","marketid":"17"},{"code":"000001","marketid":"33"}]}`

func TestSanitizeCookie(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"原样保留", "a=1; b=2", "a=1; b=2"},
		{"去掉 Cookie 前缀", "Cookie: a=1; b=2", "a=1; b=2"},
		{"小写前缀", "cookie:a=1", "a=1"},
		{"多行粘贴", "a=1;\r\nb=2\n", "a=1;b=2"},
		{"去掉尾部分号", "a=1;", "a=1"},
		{"空串", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeCookie(tc.in); got != tc.want {
				t.Fatalf("sanitizeCookie(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestVerifyTHSCookieBranches(t *testing.T) {
	transport := (&fakeTransport{}).add("getSelfStockWithMarket", []byte(thsWebFixture))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	count, err := service.VerifyTHSCookie(context.Background(), " Cookie: a=1; b=2 ")
	if err != nil {
		t.Fatalf("有效 cookie 不应报错: %v", err)
	}
	if count != 2 {
		t.Fatalf("校验标的数 = %d，期望 2", count)
	}
	if transport.countCalls("getSelfStockWithMarket") != 1 {
		t.Fatal("必须真的请求一次网页端自选（先验证再落盘）")
	}

	if _, err := service.VerifyTHSCookie(context.Background(), "  "); err == nil || !strings.Contains(err.Error(), "Cookie 不能为空") {
		t.Fatalf("空 cookie 报错文案不对: %v", err)
	}
	if _, err := service.VerifyTHSCookie(context.Background(), strings.Repeat("a", thsCookieMaxBytes+1)); err == nil || !strings.Contains(err.Error(), "过长") {
		t.Fatalf("超长 cookie 应被拒绝: %v", err)
	}

	// errorCode != 0 是「cookie 过期」的线上表现，必须落成可操作中文
	expired := (&fakeTransport{}).add("getSelfStockWithMarket", []byte(`{"errorCode":-1,"errorMsg":"未登录","result":[]}`))
	expiredService, _ := newServiceWithTransport(t, newMemStore(), expired)
	_, err = expiredService.VerifyTHSCookie(context.Background(), "a=1")
	if err == nil || !strings.Contains(err.Error(), "登录态无效") {
		t.Fatalf("过期 cookie 应提示登录态无效，实际 %v", err)
	}
}

func TestSaveTHSCookieWritesMode0600(t *testing.T) {
	transport := (&fakeTransport{}).add("getSelfStockWithMarket", []byte(thsWebFixture))
	t.Setenv(thsCookieEnv, "")
	dir := t.TempDir()
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: dir})
	service.client.http = newTestHTTPClient(transport)

	if _, err := service.SaveTHSCookie(context.Background(), "Cookie: a=1; b=2"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	path := filepath.Join(dir, thsCookieFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("登录态文件未落盘: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("文件权限 = %o，期望 600", mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != "a=1; b=2" {
		t.Fatalf("落盘内容 = %q，期望清洗后的 header", got)
	}
	if service.thsCookie() != "a=1; b=2" {
		t.Fatal("落盘后应能被 thsCookie 读回（说明 sync 会用上这份登录态）")
	}

	// 权限被外部改松后，再次保存必须收紧回 0600
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveTHSCookie(context.Background(), "a=1; b=2"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("再次保存后权限 = %o，期望 600", info.Mode().Perm())
	}
}

func TestSaveTHSCookieRejectsInvalid(t *testing.T) {
	transport := (&fakeTransport{}).add("getSelfStockWithMarket", []byte(`{"errorCode":-1,"errorMsg":"未登录","result":[]}`))
	dir := t.TempDir()
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: dir})
	service.client.http = newTestHTTPClient(transport)

	if _, err := service.SaveTHSCookie(context.Background(), "a=1"); err == nil {
		t.Fatal("校验不过必须报错")
	}
	if _, err := os.Stat(filepath.Join(dir, thsCookieFile)); !os.IsNotExist(err) {
		t.Fatal("校验不过时不得落盘")
	}
}

func TestSaveTHSCookieRespectsGuard(t *testing.T) {
	transport := (&fakeTransport{}).add("getSelfStockWithMarket", []byte(thsWebFixture))
	dir := t.TempDir()
	guard := newFakeGuard()
	guard.denyPaths = []string{dir}
	service := newTestService(t, Config{Guard: guard, Store: newMemStore(), DataDir: dir})
	service.client.http = newTestHTTPClient(transport)

	_, err := service.SaveTHSCookie(context.Background(), "a=1")
	if err == nil || !strings.Contains(err.Error(), "不在允许目录内") {
		t.Fatalf("越权路径应被 Guard 拒绝，实际 %v", err)
	}
}

// buildBinaryCookies 按实机格式造一份 Cookies.binarycookies（文件头大端、页内小端）。
func buildBinaryCookies(t *testing.T, cookies []browserCookie) []byte {
	t.Helper()
	page := buildBinaryCookiePage(t, cookies)
	out := make([]byte, 0, 12+len(page))
	out = append(out, []byte("cook")...)
	out = binary.BigEndian.AppendUint32(out, 1)
	out = binary.BigEndian.AppendUint32(out, uint32(len(page)))
	return append(out, page...)
}

func buildBinaryCookiePage(t *testing.T, cookies []browserCookie) []byte {
	t.Helper()
	records := make([][]byte, 0, len(cookies))
	for _, cookie := range cookies {
		records = append(records, buildBinaryCookieRecord(t, cookie))
	}
	headerLen := 8 + 4*len(cookies) + 4
	page := make([]byte, 0, headerLen)
	page = append(page, 0x00, 0x00, 0x01, 0x00)
	page = binary.LittleEndian.AppendUint32(page, uint32(len(cookies)))
	at := headerLen
	for _, record := range records {
		page = binary.LittleEndian.AppendUint32(page, uint32(at))
		at += len(record)
	}
	page = binary.LittleEndian.AppendUint32(page, 0)
	for _, record := range records {
		page = append(page, record...)
	}
	return page
}

func buildBinaryCookieRecord(t *testing.T, cookie browserCookie) []byte {
	t.Helper()
	payload := []byte(cookie.Domain + "\x00" + cookie.Name + "\x00" + cookie.Path + "\x00" + cookie.Value + "\x00")
	record := make([]byte, binaryCookieHeaderLen)
	urlOffset := binaryCookieHeaderLen
	nameOffset := urlOffset + len(cookie.Domain) + 1
	pathOffset := nameOffset + len(cookie.Name) + 1
	valueOffset := pathOffset + len(cookie.Path) + 1
	binary.LittleEndian.PutUint32(record[0:4], uint32(binaryCookieHeaderLen+len(payload)))
	binary.LittleEndian.PutUint32(record[16:20], uint32(urlOffset))
	binary.LittleEndian.PutUint32(record[20:24], uint32(nameOffset))
	binary.LittleEndian.PutUint32(record[24:28], uint32(pathOffset))
	binary.LittleEndian.PutUint32(record[28:32], uint32(valueOffset))
	return append(record, payload...)
}

func TestParseBinaryCookies(t *testing.T) {
	raw := buildBinaryCookies(t, []browserCookie{
		{Domain: ".10jqka.com.cn", Name: "userid", Path: "/", Value: "abc"},
		{Domain: "t.10jqka.com.cn", Name: "u_name", Path: "/", Value: "def"},
		{Domain: ".example.com", Name: "other", Path: "/", Value: "zzz"},
	})
	cookies, err := parseBinaryCookies(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(cookies) != 3 {
		t.Fatalf("cookie 数 = %d，期望 3: %+v", len(cookies), cookies)
	}
	if cookies[0].Domain != ".10jqka.com.cn" || cookies[0].Name != "userid" || cookies[0].Value != "abc" {
		t.Fatalf("首条 = %+v", cookies[0])
	}
	header := cookieHeaderFor(cookies, thsCookieDomain)
	if !strings.Contains(header, "userid=abc") || !strings.Contains(header, "u_name=def") {
		t.Fatalf("拼出的请求头 = %q", header)
	}
	if strings.Contains(header, "other") {
		t.Fatalf("不该带上别的域名的 cookie: %q", header)
	}
}

func TestParseBinaryCookiesBadInput(t *testing.T) {
	if _, err := parseBinaryCookies([]byte("cook")); err == nil {
		t.Fatal("截断文件应报错")
	}
	if _, err := parseBinaryCookies([]byte("sqlite format 3")); err == nil {
		t.Fatal("非 binarycookies 文件应报错")
	}
	// 页表本身被截断：明确报错，而不是当成「没有 cookie」
	bad := []byte("cook")
	bad = binary.BigEndian.AppendUint32(bad, 3)
	if _, err := parseBinaryCookies(bad); err == nil {
		t.Fatal("页表不完整应报错")
	}
	// 页表完整但页面正文被截断：能读多少读多少，不因为尾部破损丢掉可用 cookie，也不能 panic
	truncated := []byte("cook")
	truncated = binary.BigEndian.AppendUint32(truncated, 1)
	truncated = binary.BigEndian.AppendUint32(truncated, 9999)
	if cookies, err := parseBinaryCookies(truncated); err != nil || len(cookies) != 0 {
		t.Fatalf("正文截断应降级为空结果，实际 %v/%d", err, len(cookies))
	}
}

func TestCaptureBrowserTHSCookieFromSafari(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, safariCookieRel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := buildBinaryCookies(t, []browserCookie{{Domain: ".10jqka.com.cn", Name: "userid", Path: "/", Value: "abc"}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	service.home = home

	cookie, source, err := service.CaptureBrowserTHSCookie()
	if err != nil {
		t.Fatalf("应从 Safari 读到 cookie: %v", err)
	}
	if source != "safari" || cookie != "userid=abc" {
		t.Fatalf("抓取结果 = %q/%q", source, cookie)
	}
}

func TestCaptureBrowserTHSCookieChromeNeedsPaste(t *testing.T) {
	home := t.TempDir()
	chrome := filepath.Join(home, chromiumCookieRels[0])
	if err := os.MkdirAll(filepath.Dir(chrome), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chrome, []byte("SQLite format 3"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	service.home = home

	cookie, _, err := service.CaptureBrowserTHSCookie()
	if err == nil {
		t.Fatal("Chrome cookie 加密，不能返回成功")
	}
	if cookie != "" {
		t.Fatalf("失败时不得返回半截 cookie: %q", cookie)
	}
	if !strings.Contains(err.Error(), "粘贴") || !strings.Contains(err.Error(), "加密") {
		t.Fatalf("应明确告诉用户「请粘贴」，实际 %v", err)
	}
}

func TestCaptureBrowserTHSCookieNothingFound(t *testing.T) {
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	service.home = t.TempDir()

	_, _, err := service.CaptureBrowserTHSCookie()
	if err == nil || !strings.Contains(err.Error(), "手动粘贴") {
		t.Fatalf("没有可读 cookie 时应给出粘贴引导，实际 %v", err)
	}
}
