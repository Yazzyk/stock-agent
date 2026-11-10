package llm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// keyring 是本机 API Key 的加密载体：密钥文件 0600，AES-GCM 封装。
// 设计目标：明文 key 只在构造模型客户端的一瞬间存在于内存，不落盘、不进日志、不回显前端。
type keyring struct {
	path string

	mu   sync.RWMutex
	id   string
	aead cipher.AEAD
}

type keyringFile struct {
	ID  string `json:"id"`
	Key string `json:"key"` // base64(32 字节随机密钥)
}

// openKeyring 打开（或首次创建）密钥环。文件权限严格 0600，父目录 0700。
func openKeyring(path string) (*keyring, error) {
	if path == "" {
		return nil, errors.New("llm: 密钥环路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("llm: 创建密钥环目录失败: %w", err)
	}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var f keyringFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("llm: 密钥环文件损坏: %w", err)
		}
		key, err := base64.StdEncoding.DecodeString(f.Key)
		if err != nil {
			return nil, fmt.Errorf("llm: 密钥环内容非法: %w", err)
		}
		return newKeyring(path, f.ID, key)
	case errors.Is(err, os.ErrNotExist):
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("llm: 生成密钥失败: %w", err)
		}
		id := make([]byte, 8)
		if _, err := rand.Read(id); err != nil {
			return nil, fmt.Errorf("llm: 生成密钥 ID 失败: %w", err)
		}
		f := keyringFile{ID: hex.EncodeToString(id), Key: base64.StdEncoding.EncodeToString(key)}
		buf, _ := json.Marshal(f)
		if err := os.WriteFile(path, buf, 0o600); err != nil {
			return nil, fmt.Errorf("llm: 写入密钥环失败: %w", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("llm: 设置密钥环权限失败: %w", err)
		}
		return newKeyring(path, f.ID, key)
	default:
		return nil, fmt.Errorf("llm: 读取密钥环失败: %w", err)
	}
}

func newKeyring(path, id string, key []byte) (*keyring, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("llm: 初始化加密器失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("llm: 初始化 GCM 失败: %w", err)
	}
	return &keyring{path: path, id: id, aead: aead}, nil
}

func (k *keyring) ID() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.id
}

// Seal 返回 base64(nonce||ciphertext)。
func (k *keyring) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("llm: 生成 nonce 失败: %w", err)
	}
	out := k.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open 解密 Seal 的产物；空串视为"未配置"。
func (k *keyring) Open(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("llm: 密文解码失败: %w", err)
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	ns := k.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("llm: 密文长度非法")
	}
	plain, err := k.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("llm: 密钥解密失败（密钥环可能已被替换，请重新填写 API Key）")
	}
	return string(plain), nil
}
