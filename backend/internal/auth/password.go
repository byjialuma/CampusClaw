// Package auth 实现密码哈希、随机会话标识与服务端会话存取。
package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword 使用 bcrypt 对明文密码做单向哈希。
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword 以恒定时间方式校验明文密码与哈希是否匹配。
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
