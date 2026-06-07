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

// renderTemplate executes a Go text/template over the flow vars (same
// semantics as the workflow executor's templates).
func renderTemplate(tmplStr string, vars map[string]any) (string, error) {
	tmpl, err := template.New("").Option("missingkey=zero").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}
