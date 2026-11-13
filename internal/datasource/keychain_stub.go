//go:build !darwin || !cgo

package datasource

import "errors"

// keychainPassword 在非 macOS 或未启用 cgo 的构建里不可用；提示用户手动填写。
func keychainPassword(service, account string) (string, error) {
	return "", errors.New("当前构建不支持读取 macOS 钥匙串：请手动粘贴同花顺 Cookie")
}
