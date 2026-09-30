package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// StreamLLM 是流式生成能力（*openAIClient 满足）。
type StreamLLM interface {
	// StreamAsk 以流式方式生成回答：每收到一段非空增量即回调 onDelta，
	// 返回完整文本；中途断流或回调报错时返回已积累文本与错误，由编排层决定后续处理。
	StreamAsk(ctx context.Context, messages []Message, onDelta func(string) error) (string, error)
}

// NewStreamer 暴露流式客户端构造（与 NewOpenAIClient 同一实现）。
func NewStreamer(baseURL, apiKey, model string, timeout time.Duration) StreamLLM {
	return NewOpenAIClient(baseURL, apiKey, model, timeout)
}

// streamChunk 是 SSE 流式响应中的一帧。
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// sseScannerBufferLimit 是单帧最大字节数（超长回答的单帧增量仍远小于此值）。
const sseScannerBufferLimit = 1 << 20

// StreamAsk 以 stream=true 请求 Chat Completions，解析 SSE 帧并逐段回调。
// 上游未按 SSE 返回时（网关不支持流式），降级为整体读取、单次回调，对外契约不变。
func (c *openAIClient) StreamAsk(ctx context.Context, messages []Message, onDelta func(string) error) (string, error) {
	resp, err := c.sendChat(ctx, chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   1024,
		Stream:      true,
	})
	if err != nil {
		return "", fmt.Errorf("请求 LLM 服务失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return "", fmt.Errorf("读取 LLM 响应失败: %w", readErr)
		}
		return "", responseError(resp.StatusCode, bodyBytes)
	}

	if !isEventStream(resp) {
		return consumeWholeResponse(resp.Body, onDelta)
	}
	return consumeEventStream(resp.Body, onDelta)
}

// isEventStream 判断上游是否按 SSE 返回。
func isEventStream(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "text/event-stream")
}

// consumeWholeResponse 非流式降级：整体读取并按单次增量回调。
func consumeWholeResponse(r io.Reader, onDelta func(string) error) (string, error) {
	bodyBytes, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("读取 LLM 响应失败: %w", err)
	}
	var out chatResponse
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return "", fmt.Errorf("解析 LLM 响应失败: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("LLM 响应缺少 choices")
	}
	content := out.Choices[0].Message.Content
	if content == "" {
		return "", fmt.Errorf("LLM 响应内容为空")
	}
	if err := onDelta(content); err != nil {
		return content, err
	}
	return content, nil
}

// consumeEventStream 逐行解析 SSE 帧：data: {...} 累加 delta.content，data: [DONE] 结束。
func consumeEventStream(r io.Reader, onDelta func(string) error) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), sseScannerBufferLimit)

	var full strings.Builder
	var done bool
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // event:/注释/空行均跳过
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			done = true
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return full.String(), fmt.Errorf("解析流式响应帧失败: %w", err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return full.String(), fmt.Errorf("LLM 服务返回错误: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		full.WriteString(delta)
		if err := onDelta(delta); err != nil {
			// 回调报错（通常是客户端中断推送）即停止读取。
			return full.String(), err
		}
	}
	if err := scanner.Err(); err != nil {
		// 中途断流：返回已积累文本与错误，由编排层决定持久化策略。
		return full.String(), fmt.Errorf("读取流式响应中断: %w", err)
	}
	if !done {
		// EOF 但未收到 [DONE]：连接在生成结束前被断开，视为中途断流。
		return full.String(), fmt.Errorf("读取流式响应中断: 未收到结束标记")
	}
	if full.Len() == 0 {
		return "", fmt.Errorf("LLM 响应内容为空")
	}
	return full.String(), nil
}
