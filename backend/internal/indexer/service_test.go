package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"campusclaw/internal/embedding"
	"campusclaw/internal/model"
	"campusclaw/internal/vector"
)

// ---- fakes ----

type fakeStates struct {
	mu      sync.Mutex
	pending []int64
	ready   map[int64]int
	failed  map[int64]string
}

func newFakeStates() *fakeStates {
	return &fakeStates{ready: map[int64]int{}, failed: map[int64]string{}}
}

func (f *fakeStates) MarkPending(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, id)
	delete(f.failed, id)
	return nil
}
func (f *fakeStates) MarkReady(_ context.Context, id int64, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready[id] = n
	return nil
}
func (f *fakeStates) MarkFailed(_ context.Context, id int64, cause error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[id] = cause.Error()
	return nil
}

type fakeMaterials struct {
	m       *model.Material
	data    []byte
	getErr  error
	openErr error
}

func (f *fakeMaterials) GetByID(_ context.Context, id int64) (*model.Material, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.m.ID != id {
		return nil, errors.New("not found")
	}
	return f.m, nil
}
func (f *fakeMaterials) Open(_ *model.Material) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return io.NopCloser(sectionReader(f.data)), nil
}

func sectionReader(b []byte) io.Reader { return &byteReader{b: b} }

type byteReader struct{ b []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

type fakeEmbedder struct {
	dim   int
	err   error
	lastN int
	gate  chan struct{} // 非 nil 时，Embed 阻塞等待闸门关闭
}

func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.lastN = len(texts)
	if e.gate != nil {
		<-e.gate
	}
	if e.err != nil {
		return nil, e.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, e.dim)
		v[i%e.dim] = 1
		out[i] = v
	}
	return out, nil
}
func (e *fakeEmbedder) Dim() int { return e.dim }

// fakeQdrant 记录每个文档当前点数与调用次数。
type fakeQdrant struct {
	mu          sync.Mutex
	server      *httptest.Server
	points      map[int64]int
	upserts     int
	deletes     int
	failUpsert  bool
	lastUpserts []qdrantWirePoint
}

type qdrantWirePoint struct {
	ID      string         `json:"id"`
	Payload vector.Payload `json:"payload"`
}

func newFakeQdrant(t *testing.T, failUpsert bool) *fakeQdrant {
	t.Helper()
	f := &fakeQdrant{points: map[int64]int{}, failUpsert: failUpsert}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeQdrant) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/collections/chunks/points":
		f.upserts++
		if f.failUpsert {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": map[string]string{"error": "boom"}})
			return
		}
		var body struct {
			Points []qdrantWirePoint `json:"points"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.lastUpserts = body.Points
		if len(body.Points) > 0 {
			f.points[body.Points[0].Payload.DocumentID] = len(body.Points)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case r.Method == http.MethodPost && r.URL.Path == "/collections/chunks/points/delete":
		f.deletes++
		var body struct {
			Filter struct {
				Must []struct {
					Key   string `json:"key"`
					Match struct {
						Value int64 `json:"value"`
					} `json:"match"`
				} `json:"must"`
			} `json:"filter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Filter.Must) == 1 && body.Filter.Must[0].Key == "document_id" {
			delete(f.points, body.Filter.Must[0].Match.Value)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case r.Method == http.MethodPost && r.URL.Path == "/collections/chunks/points/count":
		var body struct {
			Filter struct {
				Must []struct {
					Key   string `json:"key"`
					Match struct {
						Value int64 `json:"value"`
					} `json:"match"`
				} `json:"must"`
			} `json:"filter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		n := 0
		if len(body.Filter.Must) == 1 {
			n = f.points[body.Filter.Must[0].Match.Value]
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]int{"count": n}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newService(st stateStore, mats materialReader, emb embedding.Embedder, baseURL string) *Service {
	return &Service{States: st, Materials: mats, Embedder: emb, Vectors: vector.New(baseURL)}
}
