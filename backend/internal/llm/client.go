// Package llm 提供 Chat Completions 客户端，调用外部大模型网关生成回答。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LLM 是知识问答的大模型生成接口。
type LLM interface {
	// Ask 将 prompt 发送给 Chat Completions 端点，返回生成的文本。
	Ask(ctx context.Context, prompt string) (string, error)
}

// openAIClient 是 OpenAI 兼容协议的 Chat Completions 客户端。
type openAIClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAIClient 创建 Chat Completions 客户端。baseURL 不应以斜杠结尾。
func NewOpenAIClient(baseURL, apiKey, model string, timeout time.Duration) LLM {
	return &openAIClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: timeout},
	}
}

// chatMessage 是 OpenAI 协议中的一条消息。
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest 是 OpenAI 兼容协议的请求体。
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

// chatResponse 是 OpenAI 兼容协议的响应体。
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// systemPrompt 约束模型只基于提供的材料片段回答。
const systemPrompt = `你是一个班级知识库助教。你只能基于下面提供的材料片段回答问题。如果片段不足以回答，请明确说明"根据已有材料无法回答该问题"。回答必须基于中文，简洁准确。`

// Ask 发送 prompt 到 Chat Completions 端点并解析回答。
func (c *openAIClient) Ask(ctx context.Context, prompt string) (string, error) {
	body := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.3,
		MaxTokens:   1024,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 LLM 服务失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取 LLM 响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var out chatResponse
		if json.Unmarshal(bodyBytes, &out) == nil && out.Error != nil && out.Error.Message != "" {
			return "", fmt.Errorf("LLM 服务返回错误: %s", out.Error.Message)
		}
		return "", fmt.Errorf("LLM 服务返回 HTTP %d", resp.StatusCode)
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
	return content, nil
}
