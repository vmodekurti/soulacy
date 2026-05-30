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
	//
	// Gemini enforces strict turn alternation:
	//   user → model → user → model → …
	// When the model emits function calls, ALL tool results must arrive in a
	// SINGLE user turn immediately after (one functionResponse part per call).
	// Emitting each tool result as its own separate user turn violates this and
	// produces a 400 "function call turn comes immediately after a user turn".
	var systemPrompt string
	contents := make([]map[string]any, 0, len(req.Messages))

	for i := 0; i < len(req.Messages); {
		m := req.Messages[i]
		switch m.Role {
		case "system":
			if systemPrompt != "" {
				systemPrompt += "\n\n"
			}
			systemPrompt += m.Content
			i++
		case "user":
			text := m.Content
			if text == "" {
				text = "." // Gemini rejects empty text parts
			}
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"text": text}},
			})
			i++
		case "assistant":
			parts := []map[string]any{}
			if m.Content != "" {
				parts = append(parts, map[string]any{"text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				part := map[string]any{
					"functionCall": map[string]any{
						"name": tc.Name,
						"args": tc.Arguments,
					},
				}
				// Gemini 2.5 thinking models: thoughtSignature is a Part-level
				// field (not nested inside functionCall). Must be echoed back
				// verbatim or Gemini returns 400 INVALID_ARGUMENT.
				if tc.ThoughtSignature != "" {
					part["thoughtSignature"] = tc.ThoughtSignature
				}
				parts = append(parts, part)
			}
			// Skip turns with no parts — Gemini rejects "parts": [].
			if len(parts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "model",
					"parts": parts,
				})
			}
			i++
		case "tool":
			// Collect ALL consecutive tool messages into one user turn so
			// Gemini sees a single functionResponse batch, not N separate turns.
			parts := []map[string]any{}
			for i < len(req.Messages) && req.Messages[i].Role == "tool" {
				tm := req.Messages[i]
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name": tm.Name,
						"response": map[string]any{
							"name":    tm.Name,
							"content": tm.Content,
						},
					},
				})
				i++
			}
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": parts,
			})
		default:
			i++
		}
	}

	// Post-process contents to satisfy Gemini's strict alternation rules:
	//   1. Merge consecutive same-role turns into one (two model turns in a row
	//      from e.g. a thinking text turn + tool-call turn cause a 400).
	//   2. Ensure the sequence starts with a user turn (Gemini rejects sequences
	//      that open with a model turn).
	if len(contents) > 0 {
		merged := make([]map[string]any, 0, len(contents))
		for _, turn := range contents {
			if len(merged) == 0 {
				merged = append(merged, turn)
				continue
			}
			last := merged[len(merged)-1]
			if last["role"] == turn["role"] {
				// Append parts from this turn into the previous same-role turn.
				if ep, ok := last["parts"].([]map[string]any); ok {
					if np, ok2 := turn["parts"].([]map[string]any); ok2 {
						last["parts"] = append(ep, np...)
					}
				}
			} else {
				merged = append(merged, turn)
			}
		}
		// Gemini requires the first turn to be from the user.
		if merged[0]["role"] != "user" {
			merged = append([]map[string]any{
				{"role": "user", "parts": []map[string]any{{"text": "."}}},
			}, merged...)
		}
		contents = merged
	}

	genCfg := map[string]any{
		"temperature": req.Temperature,
		// Disable thinking for Gemini 2.5 models. When thinking is enabled the
		// model attaches a thoughtSignature to each functionCall part; that
		// signature must be echoed back verbatim in subsequent turns or the API
		// returns 400. Disabling thinking sidesteps this entirely for tool-use
		// agents. Re-enable (remove this block) once round-trip signature
		// handling is production-hardened.
		"thinkingConfig": map[string]any{
			"thinkingBudget": 0,
		},
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

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("google: marshal request: %w", err)
	}

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
		if len(bodyBytes) == 0 {
			return nil, fmt.Errorf("google: http %d (empty response body — request may be malformed or too large)", resp.StatusCode)
		}
		return nil, fmt.Errorf("google: http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"` // internal reasoning — skip in visible content
					// thoughtSignature lives on the Part, not inside functionCall.
					// Gemini 2.5 thinking models attach it here and require it
					// to be echoed back at the same level in conversation history.
					ThoughtSignature string `json:"thoughtSignature"`
					FunctionCall     *struct {
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
		// Skip internal thought parts — they are not user-visible text.
		if part.Thought {
			continue
		}
		if part.Text != "" {
			if r.Content != "" {
				r.Content += "\n"
			}
			r.Content += part.Text
		}
		if part.FunctionCall != nil {
			r.ToolCalls = append(r.ToolCalls, message.ToolCall{
				ID:               fmt.Sprintf("call_%d_%s", i, part.FunctionCall.Name),
				Name:             part.FunctionCall.Name,
				Arguments:        part.FunctionCall.Args,
				ThoughtSignature: part.ThoughtSignature, // on the Part, not inside FunctionCall
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

// sanitizeSchemaForGemini strips JSON Schema constructs Gemini doesn't accept
// and repairs the required/properties contract that Gemini enforces strictly:
// every name in `required` must exist as a key in `properties`.
//
// Key subtlety: `properties` is NOT a schema itself — it maps property NAMES
// to sub-schemas. We must NOT recursively call sanitizeSchemaForGemini on the
// properties map as a whole (that would treat property names as schema keywords
// and strip them). Instead we iterate property names, sanitize each sub-schema,
// and re-assemble.
func sanitizeSchemaForGemini(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	allowed := map[string]bool{
		"type": true, "format": true, "description": true, "nullable": true,
		"enum": true, "items": true, "properties": true, "required": true,
		"minimum": true, "maximum": true, "minItems": true, "maxItems": true,
		"minLength": true, "maxLength": true, "pattern": true,
	}

	out := make(map[string]any, len(s))
	for k, v := range s {
		if !allowed[k] {
			continue
		}
		if k == "properties" {
			// Preserve property names; only sanitize their sub-schemas.
			if propsMap, ok := v.(map[string]any); ok {
				sanitized := make(map[string]any, len(propsMap))
				for propName, propSchema := range propsMap {
					if sm, ok := propSchema.(map[string]any); ok {
						sanitized[propName] = sanitizeSchemaForGemini(sm)
					} else {
						sanitized[propName] = propSchema
					}
				}
				out["properties"] = sanitized
			}
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

	// Gemini rejects `required` entries whose names are absent from `properties`.
	// Filter to only the intersection.
	if props, hasProps := out["properties"].(map[string]any); hasProps {
		if req, hasReq := out["required"].([]any); hasReq {
			filtered := make([]any, 0, len(req))
			for _, r := range req {
				if name, ok := r.(string); ok {
					if _, defined := props[name]; defined {
						filtered = append(filtered, name)
					}
				}
			}
			if len(filtered) > 0 {
				out["required"] = filtered
			} else {
				delete(out, "required")
			}
		}
	}

	return out
}
