package auth

import (
	"context"

	"campusclaw/internal/model"
)

type ctxKey int

const currentUserKey ctxKey = iota

// WithUser 把当前登录用户放入请求上下文。
func WithUser(ctx context.Context, u *model.User) context.Context {
	return context.WithValue(ctx, currentUserKey, u)
}

// CurrentUser 从上下文取出当前用户；不存在返回 nil。
func CurrentUser(ctx context.Context) *model.User {
	u, _ := ctx.Value(currentUserKey).(*model.User)
	return u
}
