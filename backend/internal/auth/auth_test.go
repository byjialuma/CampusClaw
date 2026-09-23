package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("S3cret!密码")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if hash == "S3cret!密码" {
		t.Fatal("密码疑似明文存储")
	}
	if !strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") {
		t.Fatalf("不是 bcrypt 哈希: %q", hash)
	}
	if !CheckPassword(hash, "S3cret!密码") {
		t.Fatal("正确密码应当校验通过")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Fatal("错误密码不应通过")
	}
}

func TestNewTokenUniqueAndShape(t *testing.T) {
	t1, err := NewToken()
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	t2, err := NewToken()
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}
	if t1 == t2 {
		t.Fatal("两次生成的 token 必须不同")
	}
	if len(t1) != 64 || len(t2) != 64 {
		t.Fatalf("hex 后长度应为 64，得到 %d/%d", len(t1), len(t2))
	}
	for _, c := range t1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("token 不是小写 hex: %q", t1)
		}
	}
}
