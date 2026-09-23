package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"campusclaw/internal/auth"
	"campusclaw/internal/model"
)

// ErrTicketInvalid 表示票据不存在、已过期或已使用（下载页据此跳登录）。
var ErrTicketInvalid = errors.New("下载凭证无效或已过期")

// TicketRepository 一次性下载票据数据访问。
type TicketRepository struct {
	DB  *sql.DB
	TTL time.Duration
}

// Create 生成 60 秒级一次性票据（TTL 由配置决定，默认 60s）。
func (r *TicketRepository) Create(ctx context.Context, handoutID, userID, classID int64) (string, error) {
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	if _, err := r.DB.ExecContext(ctx,
		`INSERT INTO download_tickets (token, handout_id, user_id, class_id, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		token, handoutID, userID, classID, now, now.Add(r.TTL)); err != nil {
		return "", err
	}
	return token, nil
}

// Consume 在事务内校验并标记票据为已使用：不存在/过期/已用均返回 ErrTicketInvalid。
// 并发或“刷新导致的第二次使用”无法再次成功。
func (r *TicketRepository) Consume(ctx context.Context, token string) (*model.Ticket, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	t := &model.Ticket{}
	var used sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT token, handout_id, user_id, class_id, expires_at, used_at
		   FROM download_tickets WHERE token = ? FOR UPDATE`,
		token).Scan(&t.Token, &t.HandoutID, &t.UserID, &t.ClassID, &t.ExpiresAt, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTicketInvalid
	}
	if err != nil {
		return nil, err
	}
	if used.Valid || time.Now().After(t.ExpiresAt) {
		return nil, ErrTicketInvalid
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE download_tickets SET used_at = ? WHERE token = ? AND used_at IS NULL`,
		time.Now(), token); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return t, nil
}

// DeleteExpired 清理所有过期票据（启动时调用）。
func (r *TicketRepository) DeleteExpired(ctx context.Context) error {
	_, err := r.DB.ExecContext(ctx,
		`DELETE FROM download_tickets WHERE expires_at < ?`, time.Now())
	return err
}
