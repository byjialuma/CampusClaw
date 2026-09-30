package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAskSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("期望 POST，得到 %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("Authorization 头错误: %s", r.Header.Get("Authorization"))
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解析请求体失败: %v", err)
		}
		if req.Model != "course-chat" {
			t.Errorf("模型名错误: %s", req.Model)
		}
		if len(req.Messages) != 2 {
			t.Errorf("消息数错误: %d", len(req.Messages))
		}
		if req.Messages[0].Role != "system" {
			t.Errorf("第一条消息应为 system，得到 %s", req.Messages[0].Role)
		}
		if req.Messages[1].Role != "user" {
			t.Errorf("第二条消息应为 user，得到 %s", req.Messages[1].Role)
		}
		if req.Temperature != 0.3 {
			t.Errorf("temperature 错误: %f", req.Temperature)
		}
		if req.MaxTokens != 1024 {
			t.Errorf("max_tokens 错误: %d", req.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "这是生成的回答"}},
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL+"/v1", "sk-test", "course-chat", 30*time.Second)
	answer, err := client.Ask(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Ask 应成功: %v", err)
	}
	if answer != "这是生成的回答" {
		t.Fatalf("回答内容错误: %q", answer)
	}
}

func TestAskNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "invalid request"},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "sk-test", "course-chat", 30*time.Second)
	_, err := client.Ask(context.Background(), "test")
	if err == nil {
		t.Fatal("非 200 应返回错误")
	}
	if !strings.Contains(err.Error(), "invalid request") {
		t.Fatalf("错误信息应包含服务端 message: %v", err)
	}
}

func TestAskNon200NoErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "sk-test", "course-chat", 30*time.Second)
	_, err := client.Ask(context.Background(), "test")
	if err == nil {
		t.Fatal("非 200 应返回错误")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("错误信息应包含状态码: %v", err)
	}
}

func TestAskInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "sk-test", "course-chat", 30*time.Second)
	_, err := client.Ask(context.Background(), "test")
	if err == nil {
		t.Fatal("无效 JSON 应返回错误")
	}
}

func TestAskEmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "sk-test", "course-chat", 30*time.Second)
	_, err := client.Ask(context.Background(), "test")
	if err == nil {
		t.Fatal("空 choices 应返回错误")
	}
	if !strings.Contains(err.Error(), "缺少 choices") {
		t.Fatalf("错误信息应说明缺少 choices: %v", err)
	}
}

func TestAskEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": ""}},
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "sk-test", "course-chat", 30*time.Second)
	_, err := client.Ask(context.Background(), "test")
	if err == nil {
		t.Fatal("空内容应返回错误")
	}
	if !strings.Contains(err.Error(), "内容为空") {
		t.Fatalf("错误信息应说明内容为空: %v", err)
	}
}

func TestAskTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "ok"}},
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL, "sk-test", "course-chat", 50*time.Millisecond)
	_, err := client.Ask(context.Background(), "test")
	if err == nil {
		t.Fatal("超时应该返回错误")
	}
}

func TestAskContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "ok"}},
			},
		})
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := NewOpenAIClient(server.URL, "sk-test", "course-chat", 30*time.Second)
	_, err := client.Ask(ctx, "test")
	if err == nil {
		t.Fatal("已取消的 context 应返回错误")
	}
}
