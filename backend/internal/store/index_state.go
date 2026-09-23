package store

import (
	"context"
	"database/sql"

	"campusclaw/internal/model"
)

// maxIndexErrorLen 是落库错误信息的最大长度（与表字段一致）。
const maxIndexErrorLen = 512

// IndexStateRepository 管理 material_index 生命周期状态。
type IndexStateRepository struct {
	DB *sql.DB
}

// MarkPending 登记/重置一份讲义为待索引（清空旧错误与分片计数）。
func (r *IndexStateRepository) MarkPending(ctx context.Context, handoutID int64) error {
	_, err := r.DB.ExecContext(ctx,
		`INSERT INTO material_index (handout_id, status, chunk_count, error, indexed_at)
		 VALUES (?, ?, 0, NULL, NULL)
		 ON DUPLICATE KEY UPDATE status = VALUES(status), chunk_count = 0,
		                         error = NULL, indexed_at = NULL`,
		handoutID, model.IndexPending)
	return err
}

// MarkReady 将讲义标记为索引完成并记录分片数。
func (r *IndexStateRepository) MarkReady(ctx context.Context, handoutID int64, chunkCount int) error {
	_, err := r.DB.ExecContext(ctx,
		`INSERT INTO material_index (handout_id, status, chunk_count, error, indexed_at)
		 VALUES (?, ?, ?, NULL, NOW(3))
		 ON DUPLICATE KEY UPDATE status = VALUES(status), chunk_count = VALUES(chunk_count),
		                         error = NULL, indexed_at = NOW(3)`,
		handoutID, model.IndexReady, chunkCount)
	return err
}

// truncateIndexError 按 rune 截断错误信息以适配 VARCHAR(512)。
func truncateIndexError(msg string) string {
	if r := []rune(msg); len(r) > maxIndexErrorLen {
		return string(r[:maxIndexErrorLen])
	}
	return msg
}

// MarkFailed 将讲义标记为索引失败并保存截断后的错误原因；文件与 handouts 记录不受影响。
func (r *IndexStateRepository) MarkFailed(ctx context.Context, handoutID int64, cause error) error {
	msg := truncateIndexError(cause.Error())
	_, err := r.DB.ExecContext(ctx,
		`INSERT INTO material_index (handout_id, status, error, indexed_at)
		 VALUES (?, ?, ?, NULL)
		 ON DUPLICATE KEY UPDATE status = VALUES(status), error = VALUES(error), indexed_at = NULL`,
		handoutID, model.IndexFailed, msg)
	return err
}

// Status 取单份讲义的索引状态；无记录时返回 model.IndexPending。
func (r *IndexStateRepository) Status(ctx context.Context, handoutID int64) (string, error) {
	var status string
	err := r.DB.QueryRowContext(ctx,
		`SELECT status FROM material_index WHERE handout_id = ?`, handoutID).Scan(&status)
	if err == sql.ErrNoRows {
		return model.IndexPending, nil
	}
	return status, err
}

// ListPendingHandoutIDs 返回需要（重新）索引的讲义：
// 无 material_index 行，或状态为 pending/failed；已 ready 的不重复索引。
func (r *IndexStateRepository) ListPendingHandoutIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT h.id
		   FROM handouts h
		   LEFT JOIN material_index mi ON mi.handout_id = h.id
		  WHERE mi.handout_id IS NULL OR mi.status IN (?, ?)
		  ORDER BY h.id`,
		model.IndexPending, model.IndexFailed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ChunkCount 返回一份讲义已索引的分片数（无记录返回 0）。
func (r *IndexStateRepository) ChunkCount(ctx context.Context, handoutID int64) (int, error) {
	var n int
	err := r.DB.QueryRowContext(ctx,
		`SELECT COALESCE(chunk_count, 0) FROM material_index WHERE handout_id = ?`,
		handoutID).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}
