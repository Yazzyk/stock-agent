package datasource

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// encryptChromiumValue 按 Chrome 的 v10 格式加密，供解密与读取测试使用。
func encryptChromiumValue(t *testing.T, key []byte, host, value string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(host))
	plain := append(sum[:], []byte(value)...)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("创建 cipher 失败: %v", err)
	}
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte(chromiumCookieIV)).CryptBlocks(out, plain)
	return append([]byte("v10"), out...)
}

// writeChromiumCookiesDB 在指定路径建一个最小 Cookies SQLite 库。
func writeChromiumCookiesDB(t *testing.T, path string, rows []struct {
	host, name string
	enc        []byte
}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cookies (host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, path TEXT)`); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT INTO cookies (host_key, name, value, encrypted_value, path) VALUES (?, ?, '', ?, '/')`, row.host, row.name, row.enc); err != nil {
			t.Fatalf("写入 cookie 失败: %v", err)
		}
	}
}

// withChromiumPassword 替换钥匙串读取入口，避免测试触发系统授权弹窗。
func withChromiumPassword(t *testing.T, password string) {
	t.Helper()
	old := chromiumKeychainPassword
	chromiumKeychainPassword = func(service, account string) (string, error) { return password, nil }
	t.Cleanup(func() { chromiumKeychainPassword = old })
}

// TestChromiumCookieDecrypt 覆盖 v10 解密（含 host 前缀剥离）与 v20 的明确报错。
func TestChromiumCookieDecrypt(t *testing.T) {
	key := chromiumCookieKey("test-password")
	if len(key) != chromiumCookieKeyLen {
		t.Fatalf("key 长度应为 %d，实际 %d", chromiumCookieKeyLen, len(key))
	}
	enc := encryptChromiumValue(t, key, ".10jqka.com.cn", "userid=abc")
	got, err := decryptChromiumValue(enc, key, ".10jqka.com.cn")
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != "userid=abc" {
		t.Fatalf("解密结果 = %q，期望 userid=abc", got)
	}

	v20 := append([]byte("v20"), bytes.Repeat([]byte{0}, 16)...)
	if _, err := decryptChromiumValue(v20, key, ".10jqka.com.cn"); err == nil || !strings.Contains(err.Error(), "v20") {
		t.Fatalf("v20 应返回可识别的错误，实际: %v", err)
	}
}

// TestChromiumCookieReadFromChrome 模拟 Chrome Default profile 的 Cookies 库并验证按域名取 cookie。
func TestChromiumCookieReadFromChrome(t *testing.T) {
	key := chromiumCookieKey("test-password")
	home := t.TempDir()
	dbPath := filepath.Join(home, chromiumBrowsers[0].DataRel, "Default", "Cookies")
	writeChromiumCookiesDB(t, dbPath, []struct {
		host, name string
		enc        []byte
	}{
		{".10jqka.com.cn", "userid", encryptChromiumValue(t, key, ".10jqka.com.cn", "abc")},
		{".10jqka.com.cn", "uservip", encryptChromiumValue(t, key, ".10jqka.com.cn", "1")},
		{".example.com", "other", encryptChromiumValue(t, key, ".example.com", "zzz")},
	})
	withChromiumPassword(t, "test-password")

	header, err := readChromiumCookieHeader(dbPath, key, "10jqka.com.cn")
	if err != nil {
		t.Fatalf("读取 Chrome cookie 失败: %v", err)
	}
	if !strings.Contains(header, "userid=abc") || !strings.Contains(header, "uservip=1") {
		t.Fatalf("cookie 头不完整: %q", header)
	}
	if strings.Contains(header, "other") {
		t.Fatalf("不得混入非目标域 cookie: %q", header)
	}
}

// TestDetectChromiumCookieStore 验证「有没有 Chromium cookie 库」的探测只报存在性、不读值。
func TestDetectChromiumCookieStore(t *testing.T) {
	home := t.TempDir()
	if _, err := detectChromiumCookieStore(home); err == nil {
		t.Fatal("没有 cookie 库时应返回错误")
	}
	dbPath := filepath.Join(home, chromiumBrowsers[0].DataRel, "Default", "Cookies")
	writeChromiumCookiesDB(t, dbPath, nil)
	found, err := detectChromiumCookieStore(home)
	if err != nil {
		t.Fatalf("应探测到 Chrome cookie 库: %v", err)
	}
	if len(found) != 1 || !strings.HasSuffix(found[0], "Cookies") {
		t.Fatalf("探测结果不符: %v", found)
	}
}
