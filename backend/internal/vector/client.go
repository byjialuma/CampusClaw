// Package vector 用 net/http 调用 Qdrant REST API：集合管理、分片写入与班级过滤检索。
package vector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"campusclaw/internal/model"
)

// Collection 是全部材料分片所在的集合名。
const Collection = "chunks"

// Point 是一个待写入的分片点。
type Point struct {
	ID      string
	Vector  []float32
	Payload Payload
}

// Payload 是分片携带的全部溯源元数据（同时写入 Qdrant payload 并随检索返回）。
type Payload struct {
	ClassID    int64         `json:"class_id"`
	DocumentID int64         `json:"document_id"`
	ChunkIndex int           `json:"chunk_index"`
	FileName   string        `json:"file_name"`
	FileType   string        `json:"file_type"`
	Text       string        `json:"text"`
	Locator    model.Locator `json:"locator"`
}

// Hit 是一条检索命中。
type Hit struct {
	ID      string
	Score   float32
	Payload Payload
}

// Client 是 Qdrant REST 客户端。
type Client struct {
	baseURL string
	http    *http.Client
}

// New 创建客户端；baseURL 形如 http://qdrant:6333。
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type statusEnvelope struct {
	Status string  `json:"status"`
	Result any     `json:"result"`
	Time   float64 `json:"time"`
}

type errorEnvelope struct {
	Status struct {
		Error string `json:"error"`
	} `json:"status"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("向量库请求失败: %w", err)
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	if resp.StatusCode/100 != 2 {
		var env errorEnvelope
		_ = dec.Decode(&env)
		if env.Status.Error != "" {
			return fmt.Errorf("向量库返回 %d: %s", resp.StatusCode, env.Status.Error)
		}
		return fmt.Errorf("向量库返回状态码 %d", resp.StatusCode)
	}
	if out != nil {
		if err := dec.Decode(out); err != nil {
			return fmt.Errorf("解析向量库响应失败: %w", err)
		}
	}
	return nil
}

// DeterministicID 由文档与分片序号生成确定性 UUID，保证重索引 upsert 幂等。
func DeterministicID(documentID int64, chunkIndex int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v1:%d:%d", documentID, chunkIndex)))
	b := sum[:16]
	// 置为 UUIDv4 样式，仅为满足 Qdrant 对 UUID 字符串的格式偏好。
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hexchars = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, x := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexchars[x>>4], hexchars[x&0x0f])
	}
	return string(out)
}
