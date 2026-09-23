// Package indexer 编排材料的向量索引作业：读文件 → 解析切块 → 向量化 → 写入 Qdrant → 落索引状态。
package indexer

import (
	"context"
	"fmt"
	"io"
	"log"

	"campusclaw/internal/embedding"
	"campusclaw/internal/ingest"
	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

// stateStore 是索引状态落库能力（*store.IndexStateRepository 满足）。
type stateStore interface {
	MarkPending(ctx context.Context, handoutID int64) error
	MarkReady(ctx context.Context, handoutID int64, chunkCount int) error
	MarkFailed(ctx context.Context, handoutID int64, cause error) error
}

// materialReader 是读取讲义记录与实体文件的能力（*store.MaterialRepository 满足）。
type materialReader interface {
	GetByID(ctx context.Context, id int64) (*model.Material, error)
	Open(m *model.Material) (io.ReadCloser, error)
}

// vectorStore 是向量库写入能力（*vector.Client 满足）。
type vectorStore interface {
	UpsertPoints(ctx context.Context, points []vector.Point) error
	DeleteByDocument(ctx context.Context, documentID int64) error
	CountByDocument(ctx context.Context, documentID int64) (int, error)
}

// Service 执行单份材料的索引作业。
type Service struct {
	States    stateStore
	Materials materialReader
	Embedder  embedding.Embedder
	Vectors   vectorStore
}

// IndexMaterial 完整索引一份材料；任何环节失败都落 failed 状态并返回错误，
// 不删除材料文件，也不改 handouts 记录。
func (s *Service) IndexMaterial(ctx context.Context, handoutID int64) (err error) {
	defer func() {
		if err != nil {
			if stateErr := s.States.MarkFailed(context.Background(), handoutID, err); stateErr != nil {
				log.Printf("材料 %d 标记 failed 失败: %v", handoutID, stateErr)
			}
		}
	}()

	m, err := s.Materials.GetByID(ctx, handoutID)
	if err != nil {
		return fmt.Errorf("读取材料记录失败: %w", err)
	}

	rc, err := s.Materials.Open(m)
	if err != nil {
		return fmt.Errorf("打开材料文件失败: %w", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("读取材料文件失败: %w", err)
	}

	chunks, err := ingest.Parse(m.OriginalName, data)
	if err != nil {
		return fmt.Errorf("解析切块失败: %w", err)
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	vecs, err := s.Embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("向量化失败: %w", err)
	}
	if len(vecs) != len(chunks) {
		return fmt.Errorf("向量化条数与分片数不符: %d != %d", len(vecs), len(chunks))
	}

	points := make([]vector.Point, len(chunks))
	for i, c := range chunks {
		points[i] = vector.Point{
			ID:     vector.DeterministicID(m.ID, c.Index),
			Vector: vecs[i],
			Payload: vector.Payload{
				ClassID:    m.ClassID,
				DocumentID: m.ID,
				ChunkIndex: c.Index,
				FileName:   m.OriginalName,
				FileType:   m.FileType,
				Text:       c.Text,
				Locator:    c.Locator,
			},
		}
	}

	// 先删旧点再写新点：切块策略变化时也不残留历史分片。
	if err := s.Vectors.DeleteByDocument(ctx, m.ID); err != nil {
		return fmt.Errorf("清理旧分片失败: %w", err)
	}
	if err := s.Vectors.UpsertPoints(ctx, points); err != nil {
		return fmt.Errorf("写入分片失败: %w", err)
	}

	if err := s.States.MarkReady(ctx, handoutID, len(points)); err != nil {
		return fmt.Errorf("写入 ready 状态失败: %w", err)
	}
	log.Printf("材料 %d 索引完成：%d 个分片（class=%d）", handoutID, len(points), m.ClassID)
	return nil
}
