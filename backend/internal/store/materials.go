package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"

	"campusclaw/internal/model"
)

// MaterialRepository 讲义数据访问；所有查询均以班级为强制条件。
type MaterialRepository struct {
	DB      *sql.DB
	Storage *Storage
}

// materialColumns 是材料查询的统一列（含索引状态，无状态行按 pending）。
const materialColumns = `h.id, h.class_id, h.uploader_user_id, h.original_name, h.stored_path,
		        h.file_type, h.size_bytes, COALESCE(u.display_name, '系统'), h.created_at,
		        COALESCE(mi.status, 'pending')`

// materialFrom 是材料查询的统一 FROM/JOIN 子句。
const materialFrom = ` FROM handouts h
		   LEFT JOIN users u ON u.id = h.uploader_user_id
		   LEFT JOIN material_index mi ON mi.handout_id = h.id`

// ListByClass 返回某班级的全部讲义（服务端班级过滤的落地点之一）。
func (r *MaterialRepository) ListByClass(ctx context.Context, classID int64) ([]model.Material, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT `+materialColumns+materialFrom+`
		  WHERE h.class_id = ?
		  ORDER BY h.created_at DESC, h.id DESC`,
		classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Material
	for rows.Next() {
		m, err := scanMaterial(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetByID 按主键取讲义；不存在返回 sql.ErrNoRows。
func (r *MaterialRepository) GetByID(ctx context.Context, id int64) (*model.Material, error) {
	row := r.DB.QueryRowContext(ctx,
		`SELECT `+materialColumns+materialFrom+`
		  WHERE h.id = ?`,
		id)
	m, err := scanMaterial(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ExistsInClass 判断指定标题的讲义是否已存在于某班（播种幂等用）。
func (r *MaterialRepository) ExistsInClass(ctx context.Context, classID int64, originalName string) (bool, error) {
	var n int
	err := r.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM handouts WHERE class_id = ? AND original_name = ?`,
		classID, originalName).Scan(&n)
	return n > 0, err
}

// CreateWithStoredFile 在已完成文件校验与落盘后登记讲义记录。
// uploaderID 可为 nil（系统预置材料）。
func (r *MaterialRepository) CreateWithStoredFile(ctx context.Context, classID int64, uploaderID *int64,
	originalName, relPath, fileType string, size int64) (int64, error) {
	res, err := r.DB.ExecContext(ctx,
		`INSERT INTO handouts (class_id, uploader_user_id, original_name, stored_path, file_type, size_bytes)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		classID, uploaderID, originalName, relPath, fileType, size)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SaveAndCreate 完成“校验 → 落盘 → 入库”，入库失败回滚删除文件。
// 班级与上传者均来自服务端会话，不接受客户端传入。
func (r *MaterialRepository) SaveAndCreate(ctx context.Context, classID, uploaderID int64,
	originalName string, data []byte) (*model.Material, error) {
	fileType, err := Validate(originalName, data)
	if err != nil {
		return nil, err
	}
	relPath, _, err := r.Storage.Save(data, fileType)
	if err != nil {
		return nil, err
	}
	id, err := r.CreateWithStoredFile(ctx, classID, &uploaderID, originalName, relPath, fileType, int64(len(data)))
	if err != nil {
		_ = r.Storage.Remove(relPath) // 不留孤儿文件
		return nil, fmt.Errorf("写入材料记录失败: %w", err)
	}
	return r.GetByID(ctx, id)
}

// Open 打开材料实体文件供内容/下载接口读取。
func (r *MaterialRepository) Open(m *model.Material) (io.ReadCloser, error) {
	return os.Open(r.Storage.AbsPath(m.StoredPath))
}

// rowScanner 抽象 *sql.Row / *sql.Rows 的 Scan。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanMaterial(s rowScanner) (model.Material, error) {
	var m model.Material
	err := s.Scan(&m.ID, &m.ClassID, &m.UploaderUserID, &m.OriginalName, &m.StoredPath,
		&m.FileType, &m.SizeBytes, &m.UploaderName, &m.CreatedAt, &m.IndexStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.Material{}, err
	}
	return m, err
}
