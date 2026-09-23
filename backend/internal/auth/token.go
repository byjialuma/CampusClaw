package auth

import (
	"crypto/rand"
	"encoding/hex"
)

// tokenBytes 是会话标识/下载票据的随机字节数（hex 后 64 字符）。
const tokenBytes = 32

// NewToken 生成 32 字节密码学随机数的 hex 标识。
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
