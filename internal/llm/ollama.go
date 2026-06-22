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
	"net/http"
	"sync/atomic"

	"github.com/soulacy/soulacy/pkg/message"
)

// OllamaProvider talks to a running Ollama instance.
type OllamaProvider struct {
	baseURL   string
	model     string
	keepAlive string
	options   map[string]any
	client    *http.Client
}

const DefaultOllamaKeepAlive = "30m"

var DefaultOllamaOptions = map[string]any{
	"num_ctx":   4096,
	"num_batch": 128,
}

// NewOllamaProvider creates a provider targeting the given Ollama base URL.
func NewOllamaProvider(baseURL, defaultModel string, keepAlive string, options map[string]any) *OllamaProvider {
	if keepAlive == "" {
		keepAlive = DefaultOllamaKeepAlive
	}
	return &OllamaProvider{
		baseURL:   baseURL,
		model:     defaultModel,
		keepAlive: keepAlive,
		options:   ollamaOptionsWithDefaults(options),
		// No hard HTTP timeout AND no response-header timeout — the engine's
		// context already governs the upper bound (per-agent run_timeout). Big
		// local models (e.g. qwen3:32b, llama3.3:70b) can take minutes to load
		// from disk on first invocation; the shared transport's 120s
		// ResponseHeaderTimeout would abort that cold load with "timeout
		// awaiting response headers". LocalHTTPClient drops the header cap while
		// keeping the warm shared connection pool.
		client: LocalHTTPClient(0),
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
		if tcs := ollamaStyleToolCalls(m.ToolCalls); tcs != nil {
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
	}
	if o.keepAlive != "" {
		body["keep_alive"] = o.keepAlive
	}
	options := cloneOptions(o.options)
	options["temperature"] = req.Temperature
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	if len(options) > 0 {
		body["options"] = options
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

	// Include tools if present (Ollama >= 0.3 supports function calling).
	// Ollama speaks the OpenAI function-calling wire format, so the tool
	// definitions and tool_choice constraint are built by the shared
	// translate.go helpers. The caller (engine) already strips ToolChoice
	// between turns so we don't trap the model in a forced-tool loop.
	if len(req.Tools) > 0 {
		body["tools"] = openAIStyleTools(req.Tools)
		applyOpenAIToolChoice(body, req.ToolChoice)
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

func cloneOptions(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func ollamaOptionsWithDefaults(in map[string]any) map[string]any {
	out := cloneOptions(DefaultOllamaOptions)
	for k, v := range in {
		out[k] = v
	}
	return out
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
