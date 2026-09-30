// Package auth 提供 JWT access token 的签发与验证。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"campusclaw/internal/model"
)

// jwtHeader 是固定的 JOSE header。
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// jwtClaims 是 JWT payload 的结构。
type jwtClaims struct {
	UserID      int64  `json:"user_id"`
	ClassID     int64  `json:"class_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	ClassName   string `json:"class_name"`
	ExpiresAt   int64  `json:"exp"`
	IssuedAt    int64  `json:"iat"`
}

// JWTService 提供 JWT access token 的签发与验签。
type JWTService struct {
	secret []byte
	expiry time.Duration
}

// NewJWTService 创建 JWT 服务；secret 至少 32 字节。
func NewJWTService(secret string, expiryHours int) (*JWTService, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET 长度必须至少 32 字节，当前 %d 字节", len(secret))
	}
	if expiryHours < 1 {
		return nil, fmt.Errorf("JWT_EXPIRY_HOURS 必须至少为 1，当前 %d", expiryHours)
	}
	return &JWTService{
		secret: []byte(secret),
		expiry: time.Duration(expiryHours) * time.Hour,
	}, nil
}

// Sign 为用户签发 JWT access token，返回 token 字符串与过期时间。
func (s *JWTService) Sign(user *model.User) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(s.expiry)

	claims := jwtClaims{
		UserID:      user.ID,
		ClassID:     user.ClassID,
		Role:        user.Role,
		DisplayName: user.DisplayName,
		ClassName:   user.ClassName,
		ExpiresAt:   expiresAt.Unix(),
		IssuedAt:    now.Unix(),
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("序列化 JWT claims 失败: %w", err)
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)

	message := jwtHeader + "." + payloadB64
	signature := s.sign(message)

	token := message + "." + signature
	return token, expiresAt, nil
}

// Verify 验签并解析 JWT access token；失败时返回具体错误。
func (s *JWTService) Verify(token string) (*model.User, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("token 格式错误")
	}
	if parts[0] != jwtHeader {
		return nil, errors.New("不支持的签名算法")
	}

	message := parts[0] + "." + parts[1]
	expectedSig := s.sign(message)
	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, errors.New("签名验证失败")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("payload 解码失败")
	}

	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errors.New("payload 解析失败")
	}

	if time.Now().Unix() > claims.ExpiresAt {
		return nil, errors.New("token 已过期")
	}

	return &model.User{
		ID:          claims.UserID,
		Username:    "", // JWT 不携带 username，仅用于内部标识
		DisplayName: claims.DisplayName,
		Role:        claims.Role,
		ClassID:     claims.ClassID,
		ClassName:   claims.ClassName,
	}, nil
}

// sign 对 message 计算 HMAC-SHA256 并 Base64URL 编码。
func (s *JWTService) sign(message string) string {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
