// openai.go — OpenAI-compatible provider (OpenAI, OpenRouter, Together, Groq,
// vLLM, and other endpoints speaking the /chat/completions wire format).
//
// Moved out of ollama.go (Story ARCH-5): the OpenAI adapter is its own family,
// not an Ollama detail. The shared OpenAI-style message/tool translation lives
// in translate.go.
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
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// OpenAIProvider is a thin wrapper for any OpenAI-compatible endpoint
// (OpenAI, Anthropic via compatibility layer, Together, Groq, etc.)
type OpenAIProvider struct {
	id                string
	baseURL           string
	apiKey            string
	model             string
	organization      string // OpenAI-Organization header (enterprise/team accounts)
	parallelToolCalls *bool  // nil=default(true), false=serialize tool calls
	client            *http.Client
}

func NewOpenAIProvider(id, baseURL, apiKey, model string) *OpenAIProvider {
	return NewOpenAIProviderWithOptions(id, baseURL, apiKey, model, "", nil)
}

// NewOpenAIProviderWithOptions creates an OpenAI-compatible provider with extra settings.
func NewOpenAIProviderWithOptions(id, baseURL, apiKey, model, organization string, parallelToolCalls *bool) *OpenAIProvider {
	return &OpenAIProvider{
		id: id, baseURL: baseURL, apiKey: apiKey, model: model,
		organization: organization, parallelToolCalls: parallelToolCalls,
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
		if tcs := openAIStyleToolCalls(m.ToolCalls); tcs != nil {
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
		"stream":      false,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}

	if len(req.Tools) > 0 {
		body["tools"] = openAIStyleTools(req.Tools)
		// parallel_tool_calls: default is true (provider default); false serializes
		// tool calls which reduces agent loops on weaker models.
		if p.parallelToolCalls != nil {
			body["parallel_tool_calls"] = *p.parallelToolCalls
		} else if strings.Contains(strings.ToLower(model), "gemini") {
			// Gemini thinking models require opaque thought signatures to be echoed
			// for every functionCall part. Some OpenAI-compatible routers expose or
			// replay that metadata inconsistently on multi-tool turns, so default
			// Gemini tool use to one call at a time unless the provider config opts
			// into a specific parallel_tool_calls value.
			body["parallel_tool_calls"] = false
		}

		// Tool-choice constraint (OpenAI / OpenRouter / Together / Groq /
		// vLLM all accept this). Same semantics as Ollama: bare strings for
		// auto/none/required, object form for a specific tool name.
		applyOpenAIToolChoice(body, req.ToolChoice)
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
	httpReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if p.organization != "" {
		httpReq.Header.Set("OpenAI-Organization", p.organization)
	}

	// Retry on transient errors (429 / 5xx / network) — OpenAI's the most
	// rate-limit-prone provider, and a single blip used to kill agent runs.
	// PRODUCTION_AUDIT → HIGH/Reliability.
	resp, err := DoWithRetry(ctx, p.client, httpReq, RetryConfig{})
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", p.id, err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: http %d: %s", p.id, resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID                    string `json:"id"`
					ThoughtSignature      string `json:"thought_signature"`
					ThoughtSignatureCamel string `json:"thoughtSignature"`
					Function              struct {
						Name                  string `json:"name"`
						Arguments             string `json:"arguments"`
						ThoughtSignature      string `json:"thought_signature"`
						ThoughtSignatureCamel string `json:"thoughtSignature"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
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
		sig := tc.ThoughtSignature
		if sig == "" {
			sig = tc.ThoughtSignatureCamel
		}
		if sig == "" {
			sig = tc.Function.ThoughtSignature
		}
		if sig == "" {
			sig = tc.Function.ThoughtSignatureCamel
		}
		r.ToolCalls = append(r.ToolCalls, toolCallWithIDAndThoughtSignature(tc.ID, tc.Function.Name, args, sig))
	}
	if strings.Contains(strings.ToLower(model), "gemini") && len(r.ToolCalls) > 1 {
		// Gemini/OpenAI-compatible routers can return one thought signature for a
		// multi-function-call turn, then fail when later calls are replayed. Run
		// Gemini tools serially; the model can request the next tool after seeing
		// the first result.
		r.ToolCalls = r.ToolCalls[:1]
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

// toolCallWithID builds a ToolCall preserving the provider-assigned id.
// OpenAI-compatible providers return stable tool_call ids we must echo back
// verbatim on the following turn, so unlike toolCallFromFunc we do not mint a
// new one here.
func toolCallWithID(id, name string, args map[string]any) message.ToolCall {
	return toolCallWithIDAndThoughtSignature(id, name, args, "")
}

func toolCallWithIDAndThoughtSignature(id, name string, args map[string]any, thoughtSignature string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Arguments: args, ThoughtSignature: thoughtSignature}
}
