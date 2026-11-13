package datasource

// Chromium 系浏览器（Chrome / Edge / Brave / Arc / Chromium）cookie 的只读提取。
//
// macOS 上的 cookie 值用浏览器自己的 Safe Storage 密码做 AES-128-CBC 加密：
// 密码条目在系统钥匙串里（例如 "Chrome Safe Storage"），PBKDF2-HMAC-SHA1
// （salt "saltysalt"、1003 轮、16 字节）派生 AES key，IV 是 16 个空格。
// Chrome 80+ 在明文前追加 32 字节 SHA256(host_key)，解密后要剥掉。
//
// 这里只读 cookie 库：先复制到临时目录（避免和正在运行的浏览器抢锁 / 漏掉 WAL），
// 再用只读 SQL 按域名查 cookie。v20（app-bound）加密无法用 Safe Storage 解开，
// 会明确提示「手动填写」，绝不静默返回空值。
//
// 注意：本文件里**没有**任何面向具体站点的抓取函数。同花顺登录态只做「检测到 Chromium 库
// 就提示用户手动粘贴」（见 ths_cookie.go），因为解密后的 cookie 需要用户明确同意才会被使用。

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	_ "modernc.org/sqlite"
)

// chromiumBrowser 描述一个 Chromium 系浏览器的 cookie 库目录与钥匙串条目。
type chromiumBrowser struct {
	Name    string
	DataRel string
	Service string
	Account string
}

// chromiumBrowsers 按常见程度排列；检测到哪个浏览器的库存在就提示哪个。
var chromiumBrowsers = []chromiumBrowser{
	{Name: "chrome", DataRel: "Library/Application Support/Google/Chrome", Service: "Chrome Safe Storage", Account: "Chrome"},
	{Name: "edge", DataRel: "Library/Application Support/Microsoft Edge", Service: "Microsoft Edge Safe Storage", Account: "Microsoft Edge"},
	{Name: "brave", DataRel: "Library/Application Support/BraveSoftware/Brave-Browser", Service: "Brave Safe Storage", Account: "Brave"},
	{Name: "arc", DataRel: "Library/Application Support/Arc/User Data", Service: "Arc Safe Storage", Account: "Arc"},
	{Name: "chromium", DataRel: "Library/Application Support/Chromium", Service: "Chromium Safe Storage", Account: "Chromium"},
}

const (
	// chromiumCookieSalt 是 Chromium 系统一使用的 PBKDF2 salt。
	chromiumCookieSalt = "saltysalt"
	// chromiumCookieIter 是 macOS 上 PBKDF2 的迭代次数（Windows 为 1）。
	chromiumCookieIter = 1003
	// chromiumCookieKeyLen 是 AES-128 的 key 长度。
	chromiumCookieKeyLen = 16
	// chromiumCookieIV 是 Chromium 固定的初始向量（16 个空格）。
	chromiumCookieIV = "                "
)

// errChromiumAppBound 表示 cookie 使用 Chrome 127+ 的 v20 app-bound 加密。
var errChromiumAppBound = errors.New("cookie 使用 v20 app-bound 加密，无法从钥匙串解密")

// chromiumKeychainPassword 是钥匙串读取入口；测试可替换，避免触发系统授权弹窗。
var chromiumKeychainPassword = keychainPassword

// detectChromiumCookieStore 检测本机是否有 Chromium 系浏览器的 cookie 库，返回库路径与原因。
// 只做「有没有」的判断，不解密也不读值（解密需要用户明确同意，见 ths_cookie.go 的提示文案）。
func detectChromiumCookieStore(home string) ([]string, error) {
	if strings.TrimSpace(home) == "" {
		return nil, errors.New("无法定位用户主目录，读不到浏览器 cookie")
	}
	found := make([]string, 0, 4)
	for _, browser := range chromiumBrowsers {
		for _, db := range chromiumCookieDBs(filepath.Join(home, browser.DataRel)) {
			found = append(found, db)
		}
	}
	if len(found) == 0 {
		return nil, errors.New("没有找到 Chrome / Edge / Brave / Arc / Chromium 的 cookie 库")
	}
	return found, nil
}

// chromiumCookieDBs 返回某个浏览器数据目录下所有 profile 的 Cookies 路径（Default 优先）。
func chromiumCookieDBs(root string) []string {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	out := make([]string, 0, 4)
	if _, err := os.Stat(filepath.Join(root, "Default", "Cookies")); err == nil {
		out = append(out, filepath.Join(root, "Default", "Cookies"))
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	profiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "Profile ") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, entry.Name(), "Cookies")); err == nil {
			profiles = append(profiles, entry.Name())
		}
	}
	sort.Strings(profiles)
	for _, name := range profiles {
		out = append(out, filepath.Join(root, name, "Cookies"))
	}
	return out
}

// chromiumCookieKey 由 Safe Storage 密码派生 AES key。
func chromiumCookieKey(password string) []byte {
	if password == "" {
		return nil
	}
	return pbkdf2.Key([]byte(password), []byte(chromiumCookieSalt), chromiumCookieIter, chromiumCookieKeyLen, sha1.New)
}

// readChromiumCookieHeader 读取单个 Cookies 库并返回指定域名后缀的 Cookie 头。
// domainSuffix 是裸域名（如 10jqka.com.cn），子域自动匹配。
func readChromiumCookieHeader(cookiesPath string, key []byte, domainSuffix string) (string, error) {
	copied, cleanup, err := copySQLiteForRead(cookiesPath)
	if err != nil {
		return "", err
	}
	defer cleanup()

	db, err := sql.Open("sqlite", copied)
	if err != nil {
		return "", fmt.Errorf("打开 cookie 库失败：%w", err)
	}
	defer db.Close()
	rows, err := db.Query(
		`SELECT host_key, name, value, encrypted_value FROM cookies WHERE host_key LIKE ?`,
		"%"+strings.TrimSpace(domainSuffix),
	)
	if err != nil {
		return "", fmt.Errorf("查询 cookie 失败：%w", err)
	}
	defer rows.Close()

	cookies := make([]browserCookie, 0, 16)
	seen := make(map[string]bool, 16)
	for rows.Next() {
		var host, name, value string
		var encrypted []byte
		if err := rows.Scan(&host, &name, &value, &encrypted); err != nil {
			continue
		}
		if name == "" || seen[name] {
			continue
		}
		plain := value
		if len(encrypted) > 0 {
			decrypted, err := decryptChromiumValue(encrypted, key, host)
			if err != nil {
				if errors.Is(err, errChromiumAppBound) {
					return "", err
				}
				continue
			}
			plain = decrypted
		}
		if plain == "" {
			continue
		}
		seen[name] = true
		cookies = append(cookies, browserCookie{Domain: host, Name: name, Path: "/", Value: plain})
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("遍历 cookie 失败：%w", err)
	}
	return cookieHeaderFor(cookies, strings.TrimSpace(domainSuffix)), nil
}

// decryptChromiumValue 解密 v10/v11 cookie；空值/明文原样返回，v20 返回可识别错误。
func decryptChromiumValue(encrypted []byte, key []byte, host string) (string, error) {
	if len(encrypted) == 0 {
		return "", nil
	}
	prefix := ""
	if len(encrypted) >= 3 {
		prefix = string(encrypted[:3])
	}
	switch prefix {
	case "v10", "v11":
		if len(key) != chromiumCookieKeyLen {
			return "", errors.New("缺少可用的 Safe Storage 密码")
		}
		payload := encrypted[3:]
		if len(payload) == 0 || len(payload)%aes.BlockSize != 0 {
			return "", errors.New("加密 cookie 长度不是 AES 块长度的整数倍")
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return "", err
		}
		plain := make([]byte, len(payload))
		cipher.NewCBCDecrypter(block, []byte(chromiumCookieIV)).CryptBlocks(plain, payload)
		plain, err = pkcs7Unpad(plain, aes.BlockSize)
		if err != nil {
			return "", err
		}
		// Chrome 80+：明文前 32 字节是 SHA256(host_key)，用于阻止跨域复用。
		if len(plain) >= sha256.Size {
			sum := sha256.Sum256([]byte(host))
			if bytes.Equal(plain[:sha256.Size], sum[:]) {
				plain = plain[sha256.Size:]
			}
		}
		return string(plain), nil
	case "v20":
		return "", errChromiumAppBound
	default:
		return string(encrypted), nil
	}
}

// pkcs7Unpad 去掉 PKCS#7 填充；填充非法时报错，避免把损坏数据当 cookie。
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("cookie 填充长度非法")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("cookie 填充字节非法")
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errors.New("cookie 填充内容非法")
		}
	}
	return data[:len(data)-pad], nil
}

// copySQLiteForRead 把 cookie 库（含 WAL/SHM）复制到临时目录，避免锁库与漏读。
func copySQLiteForRead(src string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "guanlan-cookie-db-")
	if err != nil {
		return "", nil, fmt.Errorf("创建临时目录失败：%w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dst := filepath.Join(dir, "Cookies")
	if err := copyFile(src, dst); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("复制 cookie 库失败：%w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(src + suffix); err == nil {
			_ = copyFile(src+suffix, dst+suffix)
		}
	}
	return dst, cleanup, nil
}

// copyFile 复制普通文件并保留 0600 权限（临时目录里也不放宽）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
