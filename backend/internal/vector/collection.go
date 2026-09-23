package vector

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// EnsureCollection 幂等创建 chunks 集合（Cosine 距离）与 class_id/document_id 负载索引。
func (c *Client) EnsureCollection(ctx context.Context, dim int) error {
	body := map[string]any{
		"vectors": map[string]any{"size": dim, "distance": "Cosine"},
	}
	var env struct {
		Result bool `json:"result"`
	}
	// 集合已存在时：部分 Qdrant 版本返回 200/result=true，v1.12 返回 409，
	// 文本均含 "already exists"；两种情况都按幂等成功处理。
	if err := c.do(ctx, http.MethodPut, "/collections/"+Collection, body, &env); err != nil {
		if !isAlreadyExists(err) {
			return err
		}
	}
	// 已存在的负载索引 Qdrant 以 400 报错；忽略该幂等冲突。
	for _, field := range []string{"class_id", "document_id"} {
		err := c.do(ctx, http.MethodPut, "/collections/"+Collection+"/index",
			map[string]any{"field_name": field, "field_schema": "keyword"}, nil)
		if err != nil && !isAlreadyExists(err) {
			return err
		}
	}
	return nil
}

// Ping 探测 Qdrant 健康端点，供 /health 与启动等待使用。
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Code: resp.StatusCode}
	}
	return nil
}

// StatusError 表示非 2xx 的健康探测结果。
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return "qdrant healthz 返回非 200 状态" }

func isAlreadyExists(err error) bool {
	return strings.Contains(err.Error(), "already exists")
}
