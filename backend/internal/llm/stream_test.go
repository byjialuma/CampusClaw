package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newStreamTestClient 针对测试服务器创建客户端。
func newStreamTestClient(baseURL string) StreamLLM {
	return NewStreamer(baseURL, "test-key", "course-chat", 10*time.Second)
}

// sseFrame 构造一帧 data 行。
func sseFrame(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"delta": map[string]any{"content": content}},
		},
	})
	return "data: " + string(b) + "\n\n"
}

// collectDeltas 收集全部增量回调。
func collectDeltas() (func(string) error, *[]string) {
	var got []string
	return func(s string) error {
		got = append(got, s)
		return nil
	}, &got
}

// ---------- 多 delta 流式解析 ----------

func TestStreamAskMultipleDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 请求体必须声明 stream=true 且带 Bearer 认证。
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解析请求失败: %v", err)
		}
		if !req.Stream {
			t.Error("请求体必须 stream=true")
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("认证头错误: %q", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, part := range []string{"先", "回顾", "定义"} {
			fmt.Fprint(w, sseFrame(part))
			f.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer srv.Close()

	onDelta, deltas := collectDeltas()
	full, err := newStreamTestClient(srv.URL).StreamAsk(context.Background(),
		[]Message{{Role: "user", Content: "题目"}}, onDelta)
	if err != nil {
		t.Fatalf("StreamAsk 失败: %v", err)
	}
	if full != "先回顾定义" {
		t.Fatalf("完整文本错误: %q", full)
	}
	if len(*deltas) != 3 || (*deltas)[0] != "先" || (*deltas)[2] != "定义" {
		t.Fatalf("增量回调错误: %v", *deltas)
	}
}

// SSE 帧之间的空行/注释/无内容帧都应被忽略。
func TestStreamAskSkipsNoiseFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\n\n")           // 注释
		fmt.Fprint(w, "event: ping\ndata: {}\n\n") // 无 choices 帧
		fmt.Fprint(w, sseFrame("答案"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	onDelta, deltas := collectDeltas()
	full, err := newStreamTestClient(srv.URL).StreamAsk(context.Background(),
		[]Message{{Role: "user", Content: "题目"}}, onDelta)
	if err != nil {
		t.Fatalf("StreamAsk 失败: %v", err)
	}
	if full != "答案" || len(*deltas) != 1 {
		t.Fatalf("噪声帧应被忽略，full=%q deltas=%v", full, *deltas)
	}
}

// ---------- 非流式降级 ----------

func TestStreamAskFallsBackToWholeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 网关不支持流式：忽略 stream 参数，返回整包 JSON。
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "整体回答"}},
			},
		})
	}))
	defer srv.Close()

	onDelta, deltas := collectDeltas()
	full, err := newStreamTestClient(srv.URL).StreamAsk(context.Background(),
		[]Message{{Role: "user", Content: "题目"}}, onDelta)
	if err != nil {
		t.Fatalf("降级失败: %v", err)
	}
	if full != "整体回答" {
		t.Fatalf("降级文本错误: %q", full)
	}
	if len(*deltas) != 1 || (*deltas)[0] != "整体回答" {
		t.Fatalf("降级应单次回调全文，得到 %v", *deltas)
	}
}

// ---------- 非 200 ----------

func TestStreamAskNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "上游过载"},
		})
	}))
	defer srv.Close()

	_, err := newStreamTestClient(srv.URL).StreamAsk(context.Background(),
		[]Message{{Role: "user", Content: "题目"}}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "上游过载") {
		t.Fatalf("非 200 应透出网关错误，得到 %v", err)
	}
}

// ---------- 中途断流 ----------

func TestStreamAskMidStreamBreak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseFrame("已生成部分"))
		w.(http.Flusher).Flush()
		// 不发 [DONE] 直接断开连接。
	}))
	defer srv.Close()

	onDelta, deltas := collectDeltas()
	full, err := newStreamTestClient(srv.URL).StreamAsk(context.Background(),
		[]Message{{Role: "user", Content: "题目"}}, onDelta)
	if err == nil {
		t.Fatal("中途断流必须返回错误")
	}
	if full != "已生成部分" {
		t.Fatalf("断流时应返回已积累文本，得到 %q", full)
	}
	if len(*deltas) != 1 {
		t.Fatalf("断流前应有一次回调，得到 %v", *deltas)
	}
}

// 流内 error 帧同样报错并保留已积累文本。
func TestStreamAskInStreamErrorFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseFrame("部分"))
		errFrame, _ := json.Marshal(map[string]any{
			"error": map[string]any{"message": "生成中断"},
		})
		fmt.Fprintf(w, "data: %s\n\n", errFrame)
	}))
	defer srv.Close()

	full, err := newStreamTestClient(srv.URL).StreamAsk(context.Background(),
		[]Message{{Role: "user", Content: "题目"}}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "生成中断") {
		t.Fatalf("流内错误帧应报错，得到 %v", err)
	}
	if full != "部分" {
		t.Fatalf("应保留已积累文本，得到 %q", full)
	}
}

// ---------- 回调报错（客户端中断推送） ----------

func TestStreamAskCallbackErrorStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, part := range []string{"一", "二", "三"} {
			fmt.Fprint(w, sseFrame(part))
			f.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer srv.Close()

	var got []string
	full, err := newStreamTestClient(srv.URL).StreamAsk(context.Background(),
		[]Message{{Role: "user", Content: "题目"}}, func(s string) error {
			got = append(got, s)
			if s == "二" {
				return fmt.Errorf("推送失败")
			}
			return nil
		})
	if err == nil {
		t.Fatal("回调报错应中止流")
	}
	if full != "一二" {
		t.Fatalf("应返回回调成功时的积累文本，得到 %q", full)
	}
	if len(got) != 2 {
		t.Fatalf("回调报错后不得继续，得到 %v", got)
	}
}

// ---------- context 取消 ----------

func TestStreamAskContextCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseFrame("开头"))
		w.(http.Flusher).Flush()
		<-block // 挂住响应，等待测试取消 context
	}))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := make(chan struct{})
	var once bool
	onDelta := func(string) error {
		if !once {
			once = true
			close(first)
		}
		return nil
	}

	done := make(chan struct{})
	var full string
	var err error
	go func() {
		full, err = newStreamTestClient(srv.URL).StreamAsk(ctx,
			[]Message{{Role: "user", Content: "题目"}}, onDelta)
		close(done)
	}()

	select {
	case <-first:
		cancel() // 首个增量到达后取消 context
	case <-time.After(2 * time.Second):
		t.Fatal("未收到首个增量")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 StreamAsk 未返回")
	}
	if err == nil {
		t.Fatal("context 取消必须报错")
	}
	if !strings.Contains(full, "开头") {
		t.Fatalf("取消前积累的文本应返回，得到 %q", full)
	}
}
