package server

import (
	"context"
	"net/http"
	"time"
)

// health 不经鉴权；MySQL 与 Qdrant 均可达才 200，任一不可达返回 503。
func (a *API) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dbStatus := "up"
	if err := a.DB.PingContext(ctx); err != nil {
		dbStatus = "down"
	}
	vectorStatus := "up"
	// Vectors 在生产装配中必然存在；测试未注入时视为不参与探测。
	if a.Vectors != nil {
		if err := a.Vectors.Ping(ctx); err != nil {
			vectorStatus = "down"
		}
	}

	status := http.StatusOK
	overall := "ok"
	if dbStatus == "down" || vectorStatus == "down" {
		status = http.StatusServiceUnavailable
		overall = "degraded"
	}
	writeJSON(w, status, map[string]string{
		"status": overall,
		"db":     dbStatus,
		"vector": vectorStatus,
	})
}

// fakePinger 仅供测试。
type fakePinger struct{ err error }

func (f *fakePinger) PingContext(context.Context) error { return f.err }

// fakeVectorPinger 仅供测试。
type fakeVectorPinger struct{ err error }

func (f *fakeVectorPinger) Ping(context.Context) error { return f.err }
