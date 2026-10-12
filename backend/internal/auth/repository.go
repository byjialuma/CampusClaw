package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"campusclaw/internal/model"
)

// Repository 基于 MySQL 的用户认证。
type Repository struct {
	DB *sql.DB
}

// ErrInvalidCredentials 表示用户名不存在或密码错误（对外不区分）。
var ErrInvalidCredentials = errors.New("用户名或密码错误")

// StudentRow 是学生名单展示所需的最小字段集合。
type StudentRow struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ListStudentsByClass 返回指定班级的全部学生，按加入时间升序。
// 班级标识必须来自 JWT claims，调用方负责传入。
func (r *Repository) ListStudentsByClass(ctx context.Context, classID int64) ([]StudentRow, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, username, display_name, created_at
		   FROM users
		  WHERE class_id = ? AND role = ?
		  ORDER BY created_at, id`,
		classID, model.RoleStudent,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StudentRow
	for rows.Next() {
		var s StudentRow
		if err := rows.Scan(&s.ID, &s.Username, &s.DisplayName, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Authenticate 校验账号密码，成功返回带班级信息的用户。
// 用户不存在与密码错误返回同一个错误，避免账号枚举。
func (r *Repository) Authenticate(ctx context.Context, username, password string) (*model.User, error) {
	u := &model.User{}
	var hash string
	err := r.DB.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.password_hash, u.display_name, u.role,
		        u.class_id, COALESCE(c.name, '')
		   FROM users u
		   LEFT JOIN classes c ON c.id = u.class_id
		  WHERE u.username = ?`,
		username,
	).Scan(&u.ID, &u.Username, &hash, &u.DisplayName, &u.Role, &u.ClassID, &u.ClassName)
	if errors.Is(err, sql.ErrNoRows) {
		// 执行一次等价成本的比较，降低通过耗时差异枚举用户的可能性。
		CheckPassword("$2a$10$invalidinvalidinvalidinvalidinvalidin", password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if !CheckPassword(hash, password) {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}
