package auth

import (
	"strings"
	"testing"
	"time"

	"campusclaw/internal/model"
)

func testUser() *model.User {
	return &model.User{
		ID:          1,
		Username:    "teacherA",
		DisplayName: "教师A",
		Role:        model.RoleTeacher,
		ClassID:     1,
		ClassName:   "A班",
	}
}

func TestNewJWTService(t *testing.T) {
	// 密钥过短
	if _, err := NewJWTService("short", 2); err == nil {
		t.Fatal("密钥过短应报错")
	}
	// 有效期无效
	if _, err := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 0); err == nil {
		t.Fatal("有效期为 0 应报错")
	}
	// 正常
	svc, err := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 2)
	if err != nil {
		t.Fatalf("正常创建应成功: %v", err)
	}
	if svc == nil {
		t.Fatal("返回的服务不应为 nil")
	}
}

func TestJWTSignAndVerify(t *testing.T) {
	svc, err := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 2)
	if err != nil {
		t.Fatal(err)
	}
	user := testUser()

	token, expiresAt, err := svc.Sign(user)
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	if token == "" {
		t.Fatal("token 不应为空")
	}
	if expiresAt.Before(time.Now()) {
		t.Fatal("过期时间应在未来")
	}

	got, err := svc.Verify(token)
	if err != nil {
		t.Fatalf("验签失败: %v", err)
	}
	if got.ID != user.ID || got.ClassID != user.ClassID || got.Role != user.Role || got.DisplayName != user.DisplayName || got.ClassName != user.ClassName {
		t.Fatalf("解析的用户信息不匹配: got %+v, want %+v", got, user)
	}
}

func TestJWTVerifyExpired(t *testing.T) {
	svc, err := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 1)
	if err != nil {
		t.Fatal(err)
	}
	user := testUser()

	// 手动构造一个已过期的 token
	token, _, err := svc.Sign(user)
	if err != nil {
		t.Fatal(err)
	}

	// 使用负数有效期创建已过期 token
	expiredSvc, _ := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 1)
	expiredSvc.expiry = -time.Hour // 强制过期
	expiredToken, _, err := expiredSvc.Sign(user)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Verify(expiredToken); err == nil || !strings.Contains(err.Error(), "过期") {
		t.Fatalf("过期 token 应被拒绝，得到: %v", err)
	}
	if _, err := svc.Verify(token); err != nil {
		t.Fatalf("未过期 token 应通过: %v", err)
	}
}

func TestJWTVerifyTampered(t *testing.T) {
	svc, err := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 2)
	if err != nil {
		t.Fatal(err)
	}
	user := testUser()

	token, _, err := svc.Sign(user)
	if err != nil {
		t.Fatal(err)
	}

	// 篡改 payload
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("token 格式错误")
	}
	tampered := parts[0] + ".tampered." + parts[2]
	if _, err := svc.Verify(tampered); err == nil {
		t.Fatal("篡改的 token 应被拒绝")
	}

	// 篡改签名
	tampered = parts[0] + "." + parts[1] + ".tampered"
	if _, err := svc.Verify(tampered); err == nil {
		t.Fatal("篡改签名的 token 应被拒绝")
	}
}

func TestJWTVerifyInvalidFormat(t *testing.T) {
	svc, err := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 2)
	if err != nil {
		t.Fatal(err)
	}

	cases := []string{
		"",
		"only-one-part",
		"two.parts",
		"four.parts.are.invalid",
		"header.payload.signature.extra",
	}
	for _, tc := range cases {
		if _, err := svc.Verify(tc); err == nil {
			t.Fatalf("格式错误的 token %q 应被拒绝", tc)
		}
	}
}

func TestJWTVerifyWrongAlgorithm(t *testing.T) {
	svc, err := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 2)
	if err != nil {
		t.Fatal(err)
	}
	user := testUser()

	token, _, err := svc.Sign(user)
	if err != nil {
		t.Fatal(err)
	}

	// 替换 header 为 HS512
	parts := strings.Split(token, ".")
	fakeHeader := "eyJhbGciOiJIUzUxMiIsInR5cCI6IkpXVCJ9" // {"alg":"HS512","typ":"JWT"}
	tampered := fakeHeader + "." + parts[1] + "." + parts[2]
	if _, err := svc.Verify(tampered); err == nil || !strings.Contains(err.Error(), "算法") {
		t.Fatalf("错误算法应被拒绝，得到: %v", err)
	}
}

func TestJWTVerifyWrongSecret(t *testing.T) {
	svc1, _ := NewJWTService("test-secret-key-at-least-32-bytes-long!!", 2)
	svc2, _ := NewJWTService("another-secret-key-at-least-32-bytes!!", 2)

	user := testUser()
	token, _, err := svc1.Sign(user)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc2.Verify(token); err == nil {
		t.Fatal("用不同密钥验签应失败")
	}
}
