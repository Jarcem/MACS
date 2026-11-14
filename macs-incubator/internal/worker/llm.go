// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 协议驱动适配层（6.4.9 Worker 协议适配）：
//   openai_compatible — OpenAI Chat Completions 及兼容网关（base_url + api_key + model 三要素）
//   anthropic         — Anthropic Messages API（anthropic-version 头协商，默认 2023-06-01）
// 安全约定（6.2.2 凭据安全）：模型凭据只存在于孵化器本地 env，不进 dispatch payload、不注入 Worker 上下文、不落日志。
// usage 回填：token_usage 随 execution_result 遥测上行，塔侧写 cost_records。
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const llmBodyLimit = 8 * 1024

type llmDriver struct {
	name       string // 驱动名（遥测 platform_adapter 用）
	endpoint   func() (baseURL, apiKey, model string, ok bool)
	buildReq   func(j Job, baseURL, apiKey, model string) (*http.Request, error)
	parseResp  func(body []byte) (content string, usage *Usage, err error)
}

// Usage token 用量（§6.4.13.4 遥测回填 cost_records）
type Usage struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Cached     int `json:"cached,omitempty"`
}

// failOutcome 失败便捷构造
func failOutcome(format string, a ...any) Outcome {
	return Outcome{Status: "failed", Error: fmt.Sprintf(format, a...)}
}

// runLLM 统一驱动执行：构造请求 → 2xx 取 content → usage 随结果回传
func runLLM(ctx context.Context, j Job, d llmDriver) Outcome {
	baseURL, apiKey, defaultModel, ok := d.endpoint()
	if !ok {
		return failOutcome("%s: credentials not configured on incubator (see env)", d.name)
	}
	model := strings.TrimSpace(j.Model)
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		return failOutcome("%s: model required (job.model or env)", d.name)
	}

	req, err := d.buildReq(j, baseURL, apiKey, model)
	if err != nil {
		return failOutcome("%s: %v", d.name, err)
	}
	client := &http.Client{Timeout: timeoutOf(j.TimeoutMs)}
	start := time.Now()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return Outcome{} // 回滚
		}
		return failOutcome("%s: %v", d.name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, llmBodyLimit))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return failOutcome("%s: status %d body %.200s", d.name, resp.StatusCode, string(body))
	}
	content, usage, err := d.parseResp(body)
	if err != nil {
		return failOutcome("%s: %v", d.name, err)
	}

	out := Outcome{Status: "completed"}
	payload := map[string]any{
		"mode": d.name, "model": model, "content": content,
		"duration_ms": time.Since(start).Milliseconds(),
		"truncated":   len(body) >= llmBodyLimit,
	}
	if usage != nil {
		payload["token_usage"] = usage
	}
	out.Result = mustJSON(payload)
	return out
}

// promptOf LLM 输入：{{text}}/{{deps}} 占位替换
func promptOf(j Job) string {
	p := j.Prompt
	if strings.TrimSpace(p) == "" {
		p = j.Text
	}
	return expand(p, j)
}

// ── openai_compatible 驱动 ────────────────────

var openaiCompatDriver = llmDriver{
	name: "openai_compatible",
	endpoint: func() (string, string, string, bool) {
		key := os.Getenv("MACS_OPENAI_API_KEY")
		if key == "" {
			return "", "", "", false
		}
		base := os.Getenv("MACS_OPENAI_BASE_URL")
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		return strings.TrimRight(base, "/"), key, os.Getenv("MACS_OPENAI_MODEL"), true
	},
	buildReq: func(j Job, base, key, model string) (*http.Request, error) {
		reqBody := map[string]any{
			"model": model,
			"messages": []map[string]string{
				{"role": "user", "content": promptOf(j)},
			},
		}
		if sys := j.System; sys != "" {
			reqBody["messages"] = append([]map[string]string{{"role": "system", "content": sys}},
				reqBody["messages"].([]map[string]string)...)
		}
		if j.MaxTokens > 0 {
			reqBody["max_tokens"] = j.MaxTokens
		}
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, base+"/chat/completions", bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		return req, nil
	},
	parseResp: func(body []byte) (string, *Usage, error) {
		var r struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return "", nil, err
		}
		if len(r.Choices) == 0 {
			return "", r.Usage, fmt.Errorf("empty choices")
		}
		return r.Choices[0].Message.Content, r.Usage, nil
	},
}

// ── anthropic 驱动 ────────────────────────────

var anthropicDriver = llmDriver{
	name: "anthropic",
	endpoint: func() (string, string, string, bool) {
		key := os.Getenv("MACS_ANTHROPIC_API_KEY")
		if key == "" {
			return "", "", "", false
		}
		base := os.Getenv("MACS_ANTHROPIC_BASE_URL")
		if base == "" {
			base = "https://api.anthropic.com"
		}
		return strings.TrimRight(base, "/"), key, os.Getenv("MACS_ANTHROPIC_MODEL"), true
	},
	buildReq: func(j Job, base, key, model string) (*http.Request, error) {
		reqBody := map[string]any{
			"model":      model,
			"max_tokens": j.MaxTokens,
			"messages": []map[string]string{
				{"role": "user", "content": promptOf(j)},
			},
		}
		if j.MaxTokens <= 0 {
			reqBody["max_tokens"] = 2048
		}
		if sys := j.System; sys != "" {
			reqBody["system"] = sys
		}
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, base+"/v1/messages", bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01") // §6.4.9 头协商
		return req, nil
	},
	parseResp: func(body []byte) (string, *Usage, error) {
		var r struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Usage *Usage `json:"usage"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return "", nil, err
		}
		parts := make([]string, 0, len(r.Content))
		for _, c := range r.Content {
			parts = append(parts, c.Text)
		}
		return strings.Join(parts, ""), r.Usage, nil
	},
}
