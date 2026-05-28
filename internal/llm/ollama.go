// ollama.go — Ollama provider (default, local, air-gapped).
// Ollama exposes an OpenAI-compatible /api/chat endpoint.
// This adapter communicates with it directly over HTTP/JSON.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// OllamaProvider talks to a running Ollama instance.
type OllamaProvider struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaProvider creates a provider targeting the given Ollama base URL.
func NewOllamaProvider(baseURL, defaultModel string) *OllamaProvider {
	return &OllamaProvider{
		baseURL: baseURL,
		model:   defaultModel,
		// No hard HTTP timeout — the engine's context already governs the
		// upper bound (per-agent run_timeout). A 120s client cap killed
		// requests when big local models (e.g. qwen2.5:72b) were loading
		// from disk on first invocation. Transport is the shared pool from
		// httpclient.go so concurrent requests reuse idle connections.
		client: SharedHTTPClient(0),
	}
}

func (o *OllamaProvider) ID() string { return "ollama" }

func (o *OllamaProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}

	// Build the Ollama chat request. We must preserve the full tool-call loop —
	// assistant messages carry their tool_calls and tool messages carry the tool
	// name — otherwise the model can't see that it already called a tool and will
	// loop, re-calling the same tool every turn.
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				tcs = append(tcs, map[string]any{
					"function": map[string]any{"name": tc.Name, "arguments": tc.Arguments},
				})
			}
			om["tool_calls"] = tcs
		}
		if m.Role == "tool" && m.Name != "" {
			om["tool_name"] = m.Name
		}
		msgs = append(msgs, om)
	}

	body := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   false,
		"options": map[string]any{
			"temperature": req.Temperature,
			"num_predict": req.MaxTokens,
		},
	}

	// Structured output: Ollama accepts either format:"json" (free-form JSON)
	// or format: <JSON schema> (structured outputs, Ollama >= 0.5).
	switch req.ResponseFormat {
	case "json":
		body["format"] = "json"
	case "json_schema":
		if req.JSONSchema != nil {
			body["format"] = req.JSONSchema
		} else {
			body["format"] = "json"
		}
	}

	// Include tools if present (Ollama >= 0.3 supports function calling)
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			}
		}
		body["tools"] = tools

		// Tool-choice constraint. Ollama mirrors OpenAI's schema:
		//   "auto" / "none" / "required" → raw string
		//   specific tool                → {"type":"function","function":{"name":"X"}}
		// Anything else is treated as a literal tool name. Caller (engine)
		// already strips this between turns so we don't trap the model in a
		// forced-tool loop after the first call.
		if tc := strings.TrimSpace(req.ToolChoice); tc != "" {
			switch tc {
			case "auto", "none", "required":
				body["tool_choice"] = tc
			default:
				body["tool_choice"] = map[string]any{
					"type": "function",
					"function": map[string]any{"name": tc},
				}
			}
		}
	}

	// Streaming mode: only when the caller requests it AND there are no tools
	// (streaming + tool_calls requires careful reassembly; we use the
	// non-streaming path for tool turns and fall through to streaming only on
	// the final synthesis turn when the caller opts in).
	if req.Stream && len(req.Tools) == 0 {
		body["stream"] = true
		streamPayload, _ := json.Marshal(body)
		streamReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			o.baseURL+"/api/chat", bytes.NewReader(streamPayload))
		if err != nil {
			return nil, fmt.Errorf("ollama: build stream request: %w", err)
		}
		streamReq.Header.Set("Content-Type", "application/json")
		streamResp, err := o.client.Do(streamReq)
		if err != nil {
			return nil, fmt.Errorf("ollama: stream request failed: %w", err)
		}
		if streamResp.StatusCode != http.StatusOK {
			streamResp.Body.Close()
			return nil, fmt.Errorf("ollama: stream unexpected status %d", streamResp.StatusCode)
		}
		ch := make(chan string, 64)
		go func() {
			defer close(ch)
			defer streamResp.Body.Close()
			scanner := bufio.NewScanner(streamResp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				var chunk struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
					Done bool `json:"done"`
				}
				if err := json.Unmarshal([]byte(line), &chunk); err != nil {
					continue
				}
				if chunk.Message.Content != "" {
					ch <- chunk.Message.Content
				}
				if chunk.Done {
					return
				}
			}
		}()
		return &CompletionResponse{Stream: ch}, nil
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: unexpected status %d", resp.StatusCode)
	}

	var result struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		EvalCount       int `json:"eval_count"`
		PromptEvalCount int `json:"prompt_eval_count"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	r := &CompletionResponse{
		Content:      result.Message.Content,
		InputTokens:  result.PromptEvalCount,
		OutputTokens: result.EvalCount,
	}

	// Map tool calls
	for _, tc := range result.Message.ToolCalls {
		r.ToolCalls = append(r.ToolCalls, toolCallFromFunc(tc.Function.Name, tc.Function.Arguments))
	}

	return r, nil
}

func (o *OllamaProvider) Models(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: list models: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	names := make([]string, len(result.Models))
	for i, m := range result.Models {
		names[i] = m.Name
	}
	return names, nil
}

// OpenAIProvider is a thin wrapper for any OpenAI-compatible endpoint
// (OpenAI, Anthropic via compatibility layer, Together, Groq, etc.)
type OpenAIProvider struct {
	id      string
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewOpenAIProvider(id, baseURL, apiKey, model string) *OpenAIProvider {
	return &OpenAIProvider{
		id: id, baseURL: baseURL, apiKey: apiKey, model: model,
		client: SharedHTTPClient(120 * time.Second),
	}
}

func (p *OpenAIProvider) ID() string { return p.id }

func (p *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	// Build OpenAI-style messages. CRITICAL: preserve tool_calls on assistant
	// messages and tool_call_id on tool-role messages — otherwise the model
	// can't see that it already called a tool and will loop, re-calling the
	// same tool every turn (the same trap we hit with Ollama).
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := map[string]any{"role": m.Role}
		// Content can be empty when an assistant message carries only tool_calls.
		if m.Content != "" || (m.Role != "assistant") {
			om["content"] = m.Content
		} else {
			om["content"] = nil
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				args, _ := json.Marshal(tc.Arguments)
				tcs = append(tcs, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": string(args),
					},
				})
			}
			om["tool_calls"] = tcs
		}
		if m.Role == "tool" {
			if m.ToolCallID != "" {
				om["tool_call_id"] = m.ToolCallID
			}
			if m.Name != "" {
				om["name"] = m.Name
			}
		}
		msgs = append(msgs, om)
	}

	body := map[string]any{
		"model":       model,
		"messages":    msgs,
		"temperature": req.Temperature,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}

	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": t.Name, "description": t.Description, "parameters": t.Parameters,
				},
			}
		}
		body["tools"] = tools

		// Tool-choice constraint (OpenAI / OpenRouter / Together / Groq /
		// vLLM all accept this). Same semantics as Ollama: bare strings for
		// auto/none/required, object form for a specific tool name.
		if tc := strings.TrimSpace(req.ToolChoice); tc != "" {
			switch tc {
			case "auto", "none", "required":
				body["tool_choice"] = tc
			default:
				body["tool_choice"] = map[string]any{
					"type": "function",
					"function": map[string]any{"name": tc},
				}
			}
		}
	}

	// Structured outputs.
	switch req.ResponseFormat {
	case "json":
		body["response_format"] = map[string]any{"type": "json_object"}
	case "json_schema":
		if req.JSONSchema != nil {
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "output",
					"strict": true,
					"schema": req.JSONSchema,
				},
			}
		} else {
			body["response_format"] = map[string]any{"type": "json_object"}
		}
	}

	// Streaming mode: enabled when caller opts in and no tools are present
	// (tool-call reassembly from delta chunks is complex; we use non-streaming
	// for tool turns and stream only on the final synthesis turn).
	if req.Stream && len(req.Tools) == 0 {
		body["stream"] = true
		streamPayload, _ := json.Marshal(body)
		streamReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			p.baseURL+"/chat/completions", bytes.NewReader(streamPayload))
		if err != nil {
			return nil, fmt.Errorf("%s: build stream request: %w", p.id, err)
		}
		streamReq.Header.Set("Content-Type", "application/json")
		streamReq.Header.Set("Accept", "text/event-stream")
		if p.apiKey != "" {
			streamReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		streamResp, err := p.client.Do(streamReq)
		if err != nil {
			return nil, fmt.Errorf("%s: stream request failed: %w", p.id, err)
		}
		if streamResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(streamResp.Body)
			streamResp.Body.Close()
			return nil, fmt.Errorf("%s: stream http %d: %s", p.id, streamResp.StatusCode, string(body))
		}
		ch := make(chan string, 64)
		go func() {
			defer close(ch)
			defer streamResp.Body.Close()
			scanner := bufio.NewScanner(streamResp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				if data == "[DONE]" {
					return
				}
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						FinishReason string `json:"finish_reason"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(data), &chunk); err != nil {
					continue
				}
				if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
					ch <- chunk.Choices[0].Delta.Content
				}
			}
		}()
		return &CompletionResponse{Stream: ch}, nil
	}

	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	// Retry on transient errors (429 / 5xx / network) — OpenAI's the most
	// rate-limit-prone provider, and a single blip used to kill agent runs.
	// PRODUCTION_AUDIT → HIGH/Reliability.
	resp, err := DoWithRetry(ctx, p.client, httpReq, RetryConfig{})
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", p.id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: http %d: %s", p.id, resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", p.id, err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("%s: empty choices", p.id)
	}

	r := &CompletionResponse{
		Content:      result.Choices[0].Message.Content,
		InputTokens:  result.Usage.PromptTokens,
		OutputTokens: result.Usage.CompletionTokens,
	}
	for _, tc := range result.Choices[0].Message.ToolCalls {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		r.ToolCalls = append(r.ToolCalls, toolCallWithID(tc.ID, tc.Function.Name, args))
	}
	return r, nil
}

// Models queries the /models endpoint when available (OpenAI, OpenRouter,
// Together, Groq, vLLM all expose it). Returns the real error on failure
// (PRODUCTION_AUDIT → LOW/LLM) instead of pretending the list is
// `[configured-default]` — that was masking misconfiguration in the GUI.
// The Providers page now shows the actual error.
func (p *OpenAIProvider) Models(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build /models request: %w", p.id, err)
	}
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: /models request: %w", p.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: /models returned %d: %s", p.id, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: /models decode: %w", p.id, err)
	}
	if len(out.Data) == 0 {
		return []string{p.model}, nil // empty list — keep at least the default
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// --- helpers ---

// toolCallSeq monotonically increases across all toolCallFromFunc invocations
// in the process. Combined with the tool name it produces unique CallIDs
// even when the model emits multiple parallel calls to the same tool with
// different arguments (PRODUCTION_AUDIT → HIGH: OpenAI-style providers
// reject duplicate tool_call_id on the next turn).
var toolCallSeq atomic.Uint64

func toolCallFromFunc(name string, args map[string]any) message.ToolCall {
	n := toolCallSeq.Add(1)
	return message.ToolCall{ID: fmt.Sprintf("call_%s_%d", name, n), Name: name, Arguments: args}
}

func toolCallWithID(id, name string, args map[string]any) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Arguments: args}
}
