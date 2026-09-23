package store

import (
	"errors"
	"strings"
	"testing"
)

func TestTruncateIndexError(t *testing.T) {
	short := errors.New("网络超时")
	if got := truncateIndexError(short.Error()); got != "网络超时" {
		t.Fatalf("短错误不应被截断，得到 %q", got)
	}

	long := strings.Repeat("超长错误内容", 100) // 600 rune
	got := truncateIndexError(long)
	if n := len([]rune(got)); n != maxIndexErrorLen {
		t.Fatalf("长错误应截断为 %d rune，得到 %d", maxIndexErrorLen, n)
	}

	// 截断必须按 rune 边界，不能产生乱码。
	if !strings.HasPrefix(long, got) {
		t.Fatal("截断结果应是原消息的 rune 前缀")
	}
}
