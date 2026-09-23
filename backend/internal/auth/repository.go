package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"campusclaw/internal/model"
)

// Repository 基于 MySQL 的用户认证与会话存取。
type Repository struct {
	DB  *sql.DB
	TTL time.Duration
}

// ErrInvalidCredentials 表示用户名不存在或密码错误（对外不区分）。
var ErrInvalidCredentials = errors.New("用户名或密码错误")

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

// CreateSession 创建一条服务端会话并返回随机标识。
func (r *Repository) CreateSession(ctx context.Context, userID int64) (string, error) {
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	if _, err := r.DB.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		token, userID, now, now.Add(r.TTL)); err != nil {
		return "", err
	}
	return token, nil
}

// UserForToken 依据会话标识返回当前用户；会话不存在或已过期返回 sql.ErrNoRows。
func (r *Repository) UserForToken(ctx context.Context, token string) (*model.User, error) {
	u := &model.User{}
	err := r.DB.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.display_name, u.role, u.class_id, COALESCE(c.name, '')
		   FROM sessions s
		   JOIN users u ON u.id = s.user_id
		   LEFT JOIN classes c ON c.id = u.class_id
		  WHERE s.id = ? AND s.expires_at > ?`,
		token, time.Now(),
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.ClassID, &u.ClassName)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// DeleteSession 销毁服务端会话；幂等（不存在不报错）。
func (r *Repository) DeleteSession(ctx context.Context, token string) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, token)
	return err
}
