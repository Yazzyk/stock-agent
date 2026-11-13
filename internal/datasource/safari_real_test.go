package datasource

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseRealSafariCookiesIfPresent 是本机存在 Safari cookie 文件时的真实格式回归：
// binarycookies 的「文件头大端 / 页内小端」是实机推出来的，格式一旦被 Apple 改动，
// 合成 fixture 不会报警，只有真实文件会。没有文件就跳过（CI 上恒跳过）。
// 只统计条数，不打印任何 cookie 值。
func TestParseRealSafariCookiesIfPresent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("拿不到用户主目录")
	}
	raw, err := os.ReadFile(filepath.Join(home, safariCookieRel))
	if err != nil {
		t.Skip("本机没有 Safari 的 Cookies.binarycookies")
	}
	cookies, err := parseBinaryCookies(raw)
	if err != nil {
		t.Fatalf("解析真实文件失败（Safari 格式可能已变）: %v", err)
	}
	if len(cookies) == 0 {
		t.Fatal("真实文件里应有 cookie，解析结果为空")
	}
	for _, cookie := range cookies {
		if cookie.Domain == "" || cookie.Name == "" {
			t.Fatalf("解析出空字段，偏移很可能错了: %+v", cookie)
		}
	}
	t.Logf("真实文件解析出 %d 条 cookie（不打印内容）", len(cookies))
}
