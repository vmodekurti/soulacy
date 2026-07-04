// Package reasoning defines the public contracts for Soulacy reasoning
// loops (Story E15). Extension authors implement Strategy to add custom
// loop styles (Tree of Thought, Self-Reflection, Consensus Swarms, …) and
// register them with the SDK factory registry
// (registry.RegisterReasoningStrategy); agents select them by name via the
// reasoning.strategy key in SOUL.yaml.
//
// Compatibility: per the SDK policy (sdk/README.md) these interfaces are
// frozen within a major version — additive capability arrives via extension
// interfaces + type assertion, structs are append-only with zero-value
// compatibility, and the package never gains dependencies beyond stdlib.
package reasoning

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Strategy names shipped with the host. Custom strategies use their own
// names; "auto" is resolved by the host before strategy dispatch.
const (
	StrategyAuto        = "auto"
	StrategyReAct       = "react"
	StrategyPlanExecute = "plan_execute"
)

// ToolCall is a request from the LLM to invoke a tool.
type ToolCall struct {
	Tool string `json:"tool"`
	// Input is the legacy string-only argument map. It remains for SDK
	// compatibility with older strategies and tests.
	Input map[string]string `json:"input,omitempty"`
	// Arguments carries the full JSON argument object, including nested values.
	// Hosts should prefer Arguments when present and fall back to Input.
	Arguments map[string]any `json:"arguments,omitempty"`
}

// UnmarshalJSON accepts both the original string-only input contract and full
// JSON tool arguments. Models usually emit {"input":{...}}, while some newer
// prompts emit {"arguments":{...}}; both populate Arguments.
func (c *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		Tool      string         `json:"tool"`
		Input     map[string]any `json:"input"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Tool = raw.Tool
	args := raw.Arguments
	if len(args) == 0 {
		args = raw.Input
	}
	if len(args) > 0 {
		c.Arguments = args
		c.Input = make(map[string]string, len(args))
		for k, v := range args {
			switch t := v.(type) {
			case string:
				c.Input[k] = t
			case nil:
				c.Input[k] = ""
			case bool, float64:
				c.Input[k] = fmt.Sprint(t)
			default:
				b, err := json.Marshal(t)
				if err != nil {
					return err
				}
				c.Input[k] = string(b)
			}
		}
	}
	return nil
}

// Observation is the result returned by ToolExecutor.Execute().
type Observation struct {
	// Content is the tool output, truncated to 8192 bytes before returning to
	// the LLM. Empty string is valid (e.g. write operations with no output).
	Content string `json:"content"`
	// Error, when non-nil, indicates the tool call failed. Loops wrap it
	// into Content as "tool error: <msg>" and continue — they do not abort.
	Error error `json:"-"`
	// Source is an optional provenance hint (URL, file path, tool name).
	Source string `json:"source,omitempty"`
}

// Step captures one full think-act-observe cycle.
type Step struct {
	ID       string        `json:"id"`
	Thought  string        `json:"thought"`
	Action   ToolCall      `json:"action"`
	Obs      Observation   `json:"observation"`
	Duration time.Duration `json:"duration_ms"`
}

// PlannedStep is one entry in a Plan produced by LLMBackend.Plan().
type PlannedStep struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Tool        string   `json:"tool"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

// Plan is the full decomposition produced by a plan_execute-style strategy.
type Plan struct {
	Goal  string        `json:"goal"`
	Steps []PlannedStep `json:"steps"`
}

// Config controls reasoning loop behaviour. Hosts populate it from the
// agent's SOUL.yaml reasoning block; strategies must respect the limits.
type Config struct {
	// Strategy selects the loop by registered name ("react", "plan_execute",
	// custom names). "auto" is resolved by the host before dispatch.
	Strategy string
	// MaxSteps is the hard ceiling for iterative strategies (default 8).
	MaxSteps int
	// MaxPlanSteps caps plan decomposition depth (default 6).
	MaxPlanSteps int
	// StepTimeout is the context deadline for each individual step (default 30s).
	StepTimeout time.Duration
	// TotalTimeout is the whole-task deadline (default 180s).
	TotalTimeout time.Duration
	// SystemPrompt is prepended to every LLM call.
	SystemPrompt string
	// ToolNames is the list of tool names available to this agent.
	ToolNames []string
	// OutputFormat hints Reflect() how to format the final answer
	// (e.g. "structured_markdown", "decision_brief", "plain").
	OutputFormat string
	// Flow carries the compiled graph for the "flow" strategy (Story E25).
	// nil for every other strategy. Appended field — zero-value compatible.
	Flow *FlowSpec
	// PhaseParams optionally tunes the LLM calls made by built-in reasoning
	// phases. Zero values keep the strategy defaults.
	ThinkParams   PhaseParams
	PlanParams    PhaseParams
	ReflectParams PhaseParams
}

// PhaseParams tunes one internal reasoning LLM phase.
type PhaseParams struct {
	Temperature    float64
	TopP           float64
	MaxTokens      int
	ResponseFormat string
}

// ThinkRequest is the input to LLMBackend.Think().
type ThinkRequest struct {
	TaskInput    string
	StepHistory  []Step
	SystemPrompt string
	ToolNames    []string
}

// ThinkResponse is the structured JSON the LLM must return for an iterative
// step. The backend must strip any markdown fences before unmarshalling.
type ThinkResponse struct {
	Thought     string   `json:"thought"`
	IsDone      bool     `json:"is_done"`
	Action      ToolCall `json:"action"`
	FinalAnswer string   `json:"final_answer,omitempty"`
}

// ReflectRequest is the input to LLMBackend.Reflect().
type ReflectRequest struct {
	TaskInput    string
	Steps        []Step
	SystemPrompt string
	OutputFormat string
}

// ReflectResponse is the JSON the LLM returns for a Reflect call.
// updated_rules is only populated when the agent has auto_update: true.
type ReflectResponse struct {
	Output       string `json:"output"`
	UpdatedRules string `json:"updated_rules,omitempty"`
}

// LLMBackend is the interface strategies use for all LLM calls.
//
//   - Think runs once per iterative step (hot path).
//   - Plan runs once at the start of a plan-style task.
//   - Reflect synthesises the final answer from the full step trace.
type LLMBackend interface {
	Think(ctx context.Context, req ThinkRequest) (ThinkResponse, error)
	Plan(ctx context.Context, systemPrompt, taskInput string, maxSteps int) (Plan, error)
	Reflect(ctx context.Context, req ReflectRequest) (ReflectResponse, error)
}

// ToolExecutor dispatches a ToolCall to the correct handler.
// Implementations must respect ctx.Done() and must not spawn subprocesses.
type ToolExecutor interface {
	Execute(ctx context.Context, call ToolCall) Observation
}

// Env is everything a strategy needs at execution time. The host owns
// construction of the backend and executor; strategies are pure loop logic.
type Env struct {
	Config Config
	LLM    LLMBackend
	Tools  ToolExecutor
}

// Strategy is a pluggable reasoning loop. Run must ALWAYS return a usable
// ReflectResponse — tool failures become observations, never panics or bare
// errors; degraded output beats no output. The returned steps are the
// ordered think-act-observe trace for observability surfaces.
type Strategy interface {
	Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse)
}
