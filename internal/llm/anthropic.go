// anthropic.go — Anthropic Messages API provider (Claude).
//
// Anthropic's /v1/messages is shaped differently from OpenAI's chat/completions:
//
//   * The system prompt is a top-level `system` field, not a message with
//     role:"system".
//   * Roles are only "user" and "assistant" — tool results are encoded as a
//     user-role message containing a `tool_result` content block keyed by id.
//   * Tool calls arrive as `tool_use` content blocks inside an assistant
//     message (alongside any text blocks).
//   * Streaming uses the `messages` event stream; we use the non-streaming
//     mode here for simplicity (the engine doesn't stream yet).
//
// Spec: https://docs.anthropic.com/en/api/messages
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// AnthropicProvider talks to api.anthropic.com (or a compatible base URL).
type AnthropicProvider struct {
	baseURL string // default https://api.anthropic.com
	apiKey  string
	model   string
	version string // anthropic-version header (default "2023-06-01")
	client  *http.Client
}

func NewAnthropicProvider(baseURL, apiKey, model string) *AnthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	if model == "" {
		model = "claude-3-5-sonnet-latest"
	}
	return &AnthropicProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		version: "2023-06-01",
		client:  SharedHTTPClient(180 * time.Second),
	}
}

func (p *AnthropicProvider) ID() string { return "anthropic" }

func (p *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	// Translate our flat ChatMessage list into Anthropic's
	// (system, [user/assistant content-block messages]) shape.
	var systemPrompt string
	msgs := make([]map[string]any, 0, len(req.Messages))

	flushPending := func(pending *map[string]any) {
		if pending != nil && *pending != nil {
			msgs = append(msgs, *pending)
			*pending = nil
		}
	}

	// We need to merge consecutive tool-result messages into a single user
	// message (Anthropic only allows one tool_result block per id, and groups
	// them together in one user message).
	var currentUser map[string]any

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if systemPrompt != "" {
				systemPrompt += "\n\n"
			}
			systemPrompt += m.Content
		case "user":
			flushPending(&currentUser)
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "text", "text": m.Content}},
			})
		case "assistant":
			flushPending(&currentUser)
			blocks := []map[string]any{}
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": tc.Arguments,
				})
			}
			msgs = append(msgs, map[string]any{
				"role":    "assistant",
				"content": blocks,
			})
		case "tool":
			// tool results go into a single user-role message, batched together
			if currentUser == nil {
				currentUser = map[string]any{
					"role":    "user",
					"content": []map[string]any{},
				}
			}
			currentUser["content"] = append(currentUser["content"].([]map[string]any), map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			})
		}
	}
	flushPending(&currentUser)

	body := map[string]any{
		"model":       model,
		"messages":    msgs,
		"temperature": req.Temperature,
		"max_tokens":  defaultIfZero(req.MaxTokens, 4096),
	}
	if systemPrompt != "" {
		body["system"] = systemPrompt
	}

	// Tools
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			}
		}
		body["tools"] = tools
	}

	// Structured outputs: Anthropic doesn't have a native response_format, but
	// the supported pattern is to declare a single tool that returns the schema
	// and force the model to call it. We expose this only when JSONSchema is
	// supplied; pure "json" mode falls back to a system-prompt nudge added by
	// the engine.
	if req.ResponseFormat == "json_schema" && req.JSONSchema != nil {
		structTool := map[string]any{
			"name":         "respond",
			"description":  "Return the final response in the required structured format.",
			"input_schema": req.JSONSchema,
		}
		// Append to existing tools so the model still sees the others.
		existing, _ := body["tools"].([]map[string]any)
		body["tools"] = append(existing, structTool)
		body["tool_choice"] = map[string]any{"type": "tool", "name": "respond"}
	}

	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", p.version)

	// Retry on transient errors (429 / 5xx / network). The request body is a
	// bytes.Reader so net/http auto-rewinds on retry.
	resp, err := DoWithRetry(ctx, p.client, httpReq, RetryConfig{})
	if err != nil {
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("anthropic: decode: %w", err)
	}

	r := &CompletionResponse{
		InputTokens:  result.Usage.InputTokens,
		OutputTokens: result.Usage.OutputTokens,
	}
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			if r.Content != "" {
				r.Content += "\n"
			}
			r.Content += block.Text
		case "tool_use":
			// If this is the forced-schema tool, surface its input as JSON content
			// (the engine treats it as the structured final answer).
			if req.ResponseFormat == "json_schema" && block.Name == "respond" {
				if js, err := json.Marshal(block.Input); err == nil {
					r.Content = string(js)
				}
				continue
			}
			r.ToolCalls = append(r.ToolCalls, message.ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: block.Input,
			})
		}
	}

	return r, nil
}

// Models queries Anthropic's /v1/models endpoint when an API key is set,
// falling back to a baked-in current list on error. (PRODUCTION_AUDIT →
// LOW/LLM: previously the list was always the baked-in one and would drift
// behind Anthropic's actual offering — this picks up new models automatically
// for keys that have access.)
//
// The baked-in list stays as a safety net for offline/mocked deployments
// and for the moment in startup before the request completes.
var anthropicBakedInModels = []string{
	"claude-opus-4-5",
	"claude-sonnet-4-5",
	"claude-haiku-4-5",
	"claude-3-7-sonnet-latest",
	"claude-3-5-sonnet-latest",
	"claude-3-5-haiku-latest",
	"claude-3-opus-latest",
}

func (p *AnthropicProvider) Models(ctx context.Context) ([]string, error) {
	if p.apiKey == "" {
		return anthropicBakedInModels, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
	if err != nil {
		return anthropicBakedInModels, nil
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", p.version)
	resp, err := p.client.Do(req)
	if err != nil {
		return anthropicBakedInModels, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return anthropicBakedInModels, nil
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Data) == 0 {
		return anthropicBakedInModels, nil
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func defaultIfZero(v, dflt int) int {
	if v <= 0 {
		return dflt
	}
	return v
}
