package vector

import (
	"context"
	"net/http"
)

// Search 在向量库中做相似度检索。
// class_id 过滤是强制条件：请求体必然携带 class_id must 过滤，
// 其他班级的分片在向量库层面就不可能被召回。
func (c *Client) Search(ctx context.Context, queryVec []float32, classID int64, topK int) ([]Hit, error) {
	body := map[string]any{
		// /points/query（新版 Query API）的向量字段名是 query；
		// 误用旧版 /points/search 的 "vector" 字段会被忽略，退化为无序滚动（score 全 0）。
		"query":        queryVec,
		"limit":        topK,
		"with_payload": true,
		"with_vector":  false,
		"filter": filterEnvelope{Must: []condition{{
			Key:   "class_id",
			Match: matchCondition{Value: classID},
		}}},
	}

	var env struct {
		Result struct {
			Points []struct {
				ID      any     `json:"id"`
				Score   float32 `json:"score"`
				Payload Payload `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, "/collections/"+Collection+"/points/query", body, &env); err != nil {
		return nil, err
	}

	hits := make([]Hit, 0, len(env.Result.Points))
	for _, p := range env.Result.Points {
		id, _ := p.ID.(string) // 本系统的点 ID 均为确定性 UUID 字符串。
		hits = append(hits, Hit{ID: id, Score: p.Score, Payload: p.Payload})
	}
	return hits, nil
}
