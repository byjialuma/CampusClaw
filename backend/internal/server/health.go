package server

import (
	"context"
	"net/http"
	"time"
)

// health 不经鉴权；DB 可达返回 200，否则 503（绝不假性返回 200）。
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeHealth(w, a.DB, 2*time.Second)
}

func writeHealth(w http.ResponseWriter, p pinger, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := p.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "db": "down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "db": "up"})
}

// fakePinger 仅供测试。
type fakePinger struct{ err error }

func (f *fakePinger) PingContext(context.Context) error { return f.err }
