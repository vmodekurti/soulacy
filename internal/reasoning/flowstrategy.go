// flowstrategy.go — Story E25: the "flow" reasoning strategy.
//
// Registered with the E15 registry so SOUL.yaml's reasoning.strategy: flow
// routes tasks through the agent's declarative graph (Config.Flow). Node
// actions are bridged onto env.Tools — the same policy surface as every
// other strategy. The runtime's workflow path uses RunFlow directly with
// checkpoint hooks; this strategy is the chat-triggered, hook-free route.
package reasoning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	sdkreasoning "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
)

func init() {
	registry.MustRegisterReasoningStrategy("flow", func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return flowStrategy{}, nil
	})
}

type flowStrategy struct{}

// Run executes Config.Flow over env.Tools. Per the Strategy contract it
// always returns a usable ReflectResponse — graph errors become degraded
// output, never panics.
func (flowStrategy) Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse) {
	if env.Config.Flow == nil {
		return nil, ReflectResponse{Output: "flow strategy selected but the agent declares no flow graph (workflow.nodes)"}
	}
	g, err := CompileFlow(*env.Config.Flow)
	if err != nil {
		return nil, ReflectResponse{Output: "flow graph invalid: " + err.Error()}
	}

	var steps []Step
	runNode := func(ctx context.Context, node sdkreasoning.FlowNode, renderedInput string) (json.RawMessage, error) {
		toolName := node.Tool
		if node.Kind == sdkreasoning.FlowNodeAgent {
			toolName = "agent__" + node.Agent
		}
		start := time.Now()
		obs := env.Tools.Execute(ctx, ToolCall{
			Tool:  toolName,
			Input: flowCallInput(renderedInput),
		})
		step := Step{
			ID:       node.ID,
			Thought:  fmt.Sprintf("flow node %q", node.ID),
			Action:   ToolCall{Tool: toolName, Input: flowCallInput(renderedInput)},
			Obs:      obs,
			Duration: time.Since(start),
		}
		steps = append(steps, step)
		if obs.Error != nil {
			return nil, obs.Error
		}
		return json.RawMessage(flowResultJSON(obs.Content)), nil
	}

	vars := map[string]any{"trigger": taskInput}
	out, err := RunFlow(ctx, g, vars, runNode, FlowHooks{})
	if err != nil {
		return steps, ReflectResponse{Output: "flow aborted: " + err.Error()}
	}
	return steps, ReflectResponse{Output: flowOutputString(out)}
}

// flowCallInput shapes a rendered input for ToolCall.Input. JSON objects
// with scalar values map onto the args map; everything else travels under
// the "input" key.
func flowCallInput(rendered string) map[string]string {
	if rendered == "" {
		return map[string]string{}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(rendered), &obj); err == nil {
		out := make(map[string]string, len(obj))
		for k, v := range obj {
			switch t := v.(type) {
			case string:
				out[k] = t
			default:
				b, _ := json.Marshal(v)
				out[k] = string(b)
			}
		}
		return out
	}
	return map[string]string{"input": rendered}
}

// flowResultJSON wraps tool output as JSON: pass-through when it already
// is JSON, quoted string otherwise.
func flowResultJSON(content string) string {
	if json.Valid([]byte(content)) && content != "" {
		return content
	}
	b, _ := json.Marshal(content)
	return string(b)
}

// flowOutputString renders the final node result for the reply.
func flowOutputString(out json.RawMessage) string {
	if out == nil {
		return "(flow completed with no output)"
	}
	var s string
	if err := json.Unmarshal(out, &s); err == nil {
		return s
	}
	return string(out)
}

// flowTemplateFuncs are available inside node Input / edge If templates. toJson
// (alias json) is the important one: flow vars hold PARSED JSON (Go maps and
// slices), and Go's default {{ .x }} prints those in Go syntax (map[...]), which
// is NOT valid JSON. {{ toJson .x }} emits real JSON so a python node's stdin
// (or any JSON-consuming tool) receives well-formed input.
var flowTemplateFuncs = template.FuncMap{
	"toJson": toJSONString,
	"json":   toJSONString,
	// Curated helpers models commonly reach for (Sprig-style). Having them
	// registered means a reasonable generated template (e.g. {{ pluck "url"
	// .articles }}) works instead of failing to parse with "function X not
	// defined". They operate on the PARSED JSON flow vars (maps/slices/any).
	"default": tmplDefault,
	"join":    tmplJoin,
	"first":   tmplFirst,
	"last":    tmplLast,
	"pluck":   tmplPluck,
}

// FlowTemplateFuncs exposes the template functions available inside node Input /
// edge If templates, so callers that VALIDATE templates (e.g. the Studio
// pre-save check) parse with the exact same function set the renderer uses —
// keeping "valid at save" and "renders at run" in lockstep.
func FlowTemplateFuncs() template.FuncMap { return flowTemplateFuncs }

func toJSONString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// tmplDefault returns val when it's "present" (non-nil, non-empty), else def.
// Usable as {{ default "x" .y }} or {{ .y | default "x" }}.
func tmplDefault(def, val any) any {
	switch v := val.(type) {
	case nil:
		return def
	case string:
		if strings.TrimSpace(v) == "" {
			return def
		}
	case []any:
		if len(v) == 0 {
			return def
		}
	case map[string]any:
		if len(v) == 0 {
			return def
		}
	}
	return val
}

// asSlice coerces a parsed-JSON value to []any (nil when not a list).
func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

// tmplJoin joins a list's elements with sep. Accepts either argument order —
// {{ join ", " .urls }} (Sprig) or {{ join .urls ", " }} (common slip).
func tmplJoin(a, b any) string {
	sep, parts := joinArgs(a, b)
	strs := make([]string, 0, len(parts))
	for _, p := range parts {
		strs = append(strs, fmt.Sprint(p))
	}
	return strings.Join(strs, sep)
}

// joinArgs resolves (sep, list) from two args given in either order.
func joinArgs(a, b any) (string, []any) {
	if s, ok := a.(string); ok {
		return s, asSlice(b)
	}
	if s, ok := b.(string); ok {
		return s, asSlice(a)
	}
	return "", asSlice(a)
}

func tmplFirst(v any) any {
	if s := asSlice(v); len(s) > 0 {
		return s[0]
	}
	return nil
}

func tmplLast(v any) any {
	if s := asSlice(v); len(s) > 0 {
		return s[len(s)-1]
	}
	return nil
}

// tmplPluck extracts field `key` from each map in a list. Accepts either
// argument order — {{ pluck "url" .articles }} (Sprig) or
// {{ pluck .articles "url" }} (common slip) — since both are valid syntax and
// the order is a frequent model mistake.
func tmplPluck(a, b any) []any {
	var key string
	var list []any
	if s, ok := a.(string); ok {
		key, list = s, asSlice(b)
	} else if s, ok := b.(string); ok {
		key, list = s, asSlice(a)
	} else {
		return nil
	}
	var out []any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if val, present := m[key]; present {
				out = append(out, val)
			}
		}
	}
	return out
}

// renderTemplate executes a Go text/template over the flow vars (same
// semantics as the workflow executor's templates).
func renderTemplate(tmplStr string, vars map[string]any) (string, error) {
	tmpl, err := template.New("").Funcs(flowTemplateFuncs).Option("missingkey=zero").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}
