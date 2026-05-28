// gemini.go — Google Gemini provider (generativelanguage.googleapis.com).
//
// Gemini's :generateContent endpoint shape:
//
//   * Roles: "user" and "model" (no "system" — system_instruction is a
//     top-level field, similar to Anthropic's `system`).
//   * Content is a list of "parts": text, functionCall, functionResponse,
//     inlineData (for images/audio).
//   * Tool results travel as a user-role message with a functionResponse part
//     keyed by the function name.
//   * Structured outputs: a top-level generationConfig.responseSchema field
//     (JSON Schema subset) plus responseMimeType="application/json".
//
// Spec: https://ai.google.dev/api/generate-content
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

type GeminiProvider struct {
	baseURL string // default https://generativelanguage.googleapis.com
	apiKey  string
	model   string
	client  *http.Client
}

func NewGeminiProvider(baseURL, apiKey, model string) *GeminiProvider {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	if model == "" {
		model = "gemini-2.5-pro"
	}
	return &GeminiProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		client:  SharedHTTPClient(180 * time.Second),
	}
}

func (p *GeminiProvider) ID() string { return "google" }

func (p *GeminiProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	// Translate ChatMessage list → Gemini contents + system_instruction.
	var systemPrompt string
	contents := make([]map[string]any, 0, len(req.Messages))

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if systemPrompt != "" {
				systemPrompt += "\n\n"
			}
			systemPrompt += m.Content
		case "user":
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"text": m.Content}},
			})
		case "assistant":
			parts := []map[string]any{}
			if m.Content != "" {
				parts = append(parts, map[string]any{"text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": tc.Name,
						"args": tc.Arguments,
					},
				})
			}
			contents = append(contents, map[string]any{
				"role":  "model",
				"parts": parts,
			})
		case "tool":
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []map[string]any{{
					"functionResponse": map[string]any{
						"name": m.Name,
						"response": map[string]any{
							"name":    m.Name,
							"content": m.Content,
						},
					},
				}},
			})
		}
	}

	genCfg := map[string]any{
		"temperature": req.Temperature,
	}
	if req.MaxTokens > 0 {
		genCfg["maxOutputTokens"] = req.MaxTokens
	}

	// Structured outputs
	switch req.ResponseFormat {
	case "json":
		genCfg["responseMimeType"] = "application/json"
	case "json_schema":
		genCfg["responseMimeType"] = "application/json"
		if req.JSONSchema != nil {
			genCfg["responseSchema"] = sanitizeSchemaForGemini(req.JSONSchema)
		}
	}

	body := map[string]any{
		"contents":         contents,
		"generationConfig": genCfg,
	}
	if systemPrompt != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": systemPrompt}},
		}
	}
	if len(req.Tools) > 0 {
		funcs := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			funcs[i] = map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  sanitizeSchemaForGemini(t.Parameters),
			}
		}
		body["tools"] = []map[string]any{{"functionDeclarations": funcs}}
	}

	payload, _ := json.Marshal(body)

	// Use the x-goog-api-key header rather than a ?key=... URL param so the
	// key can't end up in any access log along the outbound chain.
	// (PRODUCTION_AUDIT → HIGH/Security)
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent",
		p.baseURL, url.PathEscape(model))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.apiKey)

	resp, err := DoWithRetry(ctx, p.client, httpReq, RetryConfig{})
	if err != nil {
		return nil, fmt.Errorf("google: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("google: http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall,omitempty"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("google: decode: %w", err)
	}

	r := &CompletionResponse{
		InputTokens:  result.UsageMetadata.PromptTokenCount,
		OutputTokens: result.UsageMetadata.CandidatesTokenCount,
	}
	if len(result.Candidates) == 0 {
		return r, nil
	}

	for i, part := range result.Candidates[0].Content.Parts {
		if part.Text != "" {
			if r.Content != "" {
				r.Content += "\n"
			}
			r.Content += part.Text
		}
		if part.FunctionCall != nil {
			r.ToolCalls = append(r.ToolCalls, message.ToolCall{
				ID:        fmt.Sprintf("call_%d_%s", i, part.FunctionCall.Name),
				Name:      part.FunctionCall.Name,
				Arguments: part.FunctionCall.Args,
			})
		}
	}
	return r, nil
}

// Models lists the available Gemini models for this API key.
func (p *GeminiProvider) Models(ctx context.Context) ([]string, error) {
	// Header auth, same rationale as Complete().
	endpoint := fmt.Sprintf("%s/v1beta/models", p.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return []string{p.model}, nil
	}
	httpReq.Header.Set("x-goog-api-key", p.apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return []string{p.model}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return []string{p.model}, nil
	}
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return []string{p.model}, nil
	}
	ids := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		// Only models that can do generateContent are useful here.
		ok := false
		for _, meth := range m.SupportedGenerationMethods {
			if meth == "generateContent" {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		// Strip "models/" prefix.
		name := m.Name
		if len(name) > 7 && name[:7] == "models/" {
			name = name[7:]
		}
		ids = append(ids, name)
	}
	if len(ids) == 0 {
		return []string{p.model}, nil
	}
	return ids, nil
}

// sanitizeSchemaForGemini strips JSON Schema constructs Gemini doesn't accept:
// $schema, additionalProperties (in some cases), and any non-allowlisted keys.
// This is permissive — we just drop the obvious offenders. Most user-authored
// schemas pass through unchanged.
func sanitizeSchemaForGemini(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	out := make(map[string]any, len(s))
	allowed := map[string]bool{
		"type": true, "format": true, "description": true, "nullable": true,
		"enum": true, "items": true, "properties": true, "required": true,
		"minimum": true, "maximum": true, "minItems": true, "maxItems": true,
		"minLength": true, "maxLength": true, "pattern": true,
	}
	for k, v := range s {
		if !allowed[k] {
			continue
		}
		switch vv := v.(type) {
		case map[string]any:
			out[k] = sanitizeSchemaForGemini(vv)
		case []any:
			arr := make([]any, len(vv))
			for i, item := range vv {
				if m, ok := item.(map[string]any); ok {
					arr[i] = sanitizeSchemaForGemini(m)
				} else {
					arr[i] = item
				}
			}
			out[k] = arr
		default:
			out[k] = v
		}
	}
	return out
}
