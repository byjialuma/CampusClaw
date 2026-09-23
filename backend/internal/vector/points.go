package vector

import (
	"context"
	"net/http"
)

type qdrantPoint struct {
	ID      string    `json:"id"`
	Vector  []float32 `json:"vector"`
	Payload Payload   `json:"payload"`
}

type filterEnvelope struct {
	Must []condition `json:"must"`
}

type condition struct {
	Key   string         `json:"key"`
	Match matchCondition `json:"match"`
}

type matchCondition struct {
	Value any `json:"value"`
}

func documentFilter(documentID int64) map[string]any {
	return map[string]any{
		"filter": filterEnvelope{Must: []condition{{
			Key:   "document_id",
			Match: matchCondition{Value: documentID},
		}}},
	}
}

// UpsertPoints 同步写入（wait=true）分片点；同 ID 覆盖，重索引幂等。
func (c *Client) UpsertPoints(ctx context.Context, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	qp := make([]qdrantPoint, len(points))
	for i, p := range points {
		qp[i] = qdrantPoint{ID: p.ID, Vector: p.Vector, Payload: p.Payload}
	}
	return c.do(ctx, http.MethodPut, "/collections/"+Collection+"/points?wait=true",
		map[string]any{"points": qp}, nil)
}

// DeleteByDocument 删除某文档的全部旧分片（切块数变化时也无残留）。
func (c *Client) DeleteByDocument(ctx context.Context, documentID int64) error {
	body := documentFilter(documentID)
	return c.do(ctx, http.MethodPost, "/collections/"+Collection+"/points/delete?wait=true", body, nil)
}

// CountByDocument 返回某文档当前的分片点数（重建幂等校验用）。
func (c *Client) CountByDocument(ctx context.Context, documentID int64) (int, error) {
	body := documentFilter(documentID)
	body["exact"] = true
	var env struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if err := c.do(ctx, http.MethodPost, "/collections/"+Collection+"/points/count", body, &env); err != nil {
		return 0, err
	}
	return env.Result.Count, nil
}
