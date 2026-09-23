package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strings"
	"unicode"
)

// stubEmbedder 是确定性的假向量化器：
// 用「词袋」（拉丁数字词 + 中日韩双字组）哈希到固定维度并 L2 归一化，
// 相同/共享相同词项的文本得到相近向量。仅供测试与离线环境，
// 不具备真实语义泛化能力，禁止用于生产。
type stubEmbedder struct {
	dim int
}

func newStubEmbedder(dim int) *stubEmbedder {
	return &stubEmbedder{dim: dim}
}

func (e *stubEmbedder) Dim() int { return e.dim }

func (e *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = stubVector(t, e.dim)
	}
	return out, nil
}

// stubTerms 提取词项：连续拉丁/数字词（小写）与 CJK 相邻双字组。
func stubTerms(s string) []string {
	var terms []string
	var latin strings.Builder
	flushLatin := func() {
		if latin.Len() > 0 {
			terms = append(terms, latin.String())
			latin.Reset()
		}
	}

	runes := []rune(s)
	var cjk []rune
	flushCJK := func() {
		if len(cjk) == 1 {
			terms = append(terms, "u:"+string(cjk))
		}
		cjk = nil
	}

	for _, r := range runes {
		switch {
		case isLatinOrDigit(r):
			flushCJK()
			latin.WriteRune(unicode.ToLower(r))
		case unicode.Is(unicode.Han, r):
			flushLatin()
			cjk = append(cjk, r)
			if len(cjk) == 2 {
				terms = append(terms, "b:"+string(cjk))
				cjk = cjk[1:] // 滑动双字组
			}
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return terms
}

func isLatinOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func stubVector(text string, dim int) []float32 {
	v := make([]float32, dim)
	for _, term := range stubTerms(text) {
		sum := sha256.Sum256([]byte(term))
		idx := binary.BigEndian.Uint64(sum[0:8]) % uint64(dim)
		if sum[8]&1 == 1 {
			v[idx]++
		} else {
			v[idx]--
		}
	}

	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return v // 无词项（空文本）的零向量
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= scale
	}
	return v
}
