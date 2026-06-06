// Package reasoning implements agentic loop strategies for Soulacy agents:
//
//   - StrategyAuto        (recommended) — detects the right strategy automatically.
//     Simple questions finish in a single LLM call (zero overhead). Multi-step
//     tasks run ReAct. Planning tasks run Plan-Execute.
//   - StrategyReAct       — iterative Thought → Action → Observation cycles.
//   - StrategyPlanExecute — LLM decomposes up-front, then executes steps in order.
//
// Tool failures never crash the loop. Errors become observations and the LLM
// adapts; Reflect() always runs so there is always a final output.
package reasoning

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LoopStrategy selects the execution mode.
type LoopStrategy string

const (
	// StrategyAuto is the recommended default. It detects the right strategy
	// from the task text so users never need to configure react vs plan_execute.
	//
	// Detection rules (in priority order):
	//  1. Planning keywords ("plan", "decide", "strategy", "roadmap", "compare",
	//     "pros and cons", "should I") → StrategyPlanExecute
	//  2. Research/action keywords ("research", "find", "search", "look up",
	//     "what is", "how does", "latest") → StrategyReAct
	//  3. Simple task (answer fits in one turn, no tools needed) → single-call
	//     ReAct that returns IsDone=true on step 1 (zero tool overhead)
	StrategyAuto LoopStrategy = "auto"
	// StrategyReAct runs interleaved think/act/observe cycles.
	StrategyReAct LoopStrategy = "react"
	// StrategyPlanExecute decomposes the task into a plan then executes it.
	StrategyPlanExecute LoopStrategy = "plan_execute"
)

// ToolCall is a request from the LLM to invoke a tool.
type ToolCall struct {
	Tool  string            `json:"tool"`
	Input map[string]string `json:"input"`
}

// Observation is the result returned by ToolExecutor.Execute().
type Observation struct {
	// Content is the tool output, truncated to 8192 bytes before returning to
	// the LLM. Empty string is valid (e.g. write operations with no output).
	Content string `json:"content"`
	// Error, when non-nil, indicates the tool call failed. The loop wraps it
	// into Content as "tool error: <msg>" and continues — it does not abort.
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

// Plan is the full decomposition produced by a plan_execute agent.
type Plan struct {
	Goal  string        `json:"goal"`
	Steps []PlannedStep `json:"steps"`
}

// Result is the output of Loop.Run().
type Result struct {
	// Output is the final synthesised answer from LLMBackend.Reflect().
	Output string `json:"output"`
	// Steps is the ordered trace of every think-act-observe cycle.
	Steps []Step `json:"steps"`
	// Confident is false when any step recorded a tool error.
	Confident bool `json:"confident"`
	// Duration is the total wall-clock time for the run.
	Duration time.Duration `json:"duration"`
}

// LoopConfig controls the reasoning loop behaviour.
type LoopConfig struct {
	// Strategy selects react or plan_execute.
	Strategy LoopStrategy
	// MaxSteps is the hard ceiling for ReAct iterations (default 8).
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
}

// ─── Interfaces ───────────────────────────────────────────────────────────────

// ThinkRequest is the input to LLMBackend.Think().
type ThinkRequest struct {
	TaskInput    string
	StepHistory  []Step
	SystemPrompt string
	ToolNames    []string
}

// ThinkResponse is the structured JSON the LLM must return for a ReAct step.
// The backend must strip any markdown fences before unmarshalling.
//
// JSON schema:
//
//	{
//	  "thought":      "...",
//	  "is_done":      false,
//	  "action":       { "tool": "web_search", "input": { "query": "..." } },
//	  "final_answer": ""
//	}
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
//
// JSON schema:
//
//	{
//	  "output":        "...",
//	  "updated_rules": "..."
//	}
type ReflectResponse struct {
	Output       string `json:"output"`
	UpdatedRules string `json:"updated_rules,omitempty"`
}

// LLMBackend is the interface the Loop uses for all LLM calls.
//
//   - Think runs once per ReAct step (hot path).
//   - Plan runs once at the start of a plan_execute task.
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

// ─── Loop ─────────────────────────────────────────────────────────────────────

// Loop is the main reasoning engine. Create one per task run (or reuse across
// runs — it carries no mutable per-run state).
type Loop struct {
	cfg      LoopConfig
	llm      LLMBackend
	executor ToolExecutor
}

// New creates a Loop. Both llm and executor must be non-nil.
func New(cfg LoopConfig, llm LLMBackend, executor ToolExecutor) *Loop {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 8
	}
	if cfg.MaxPlanSteps <= 0 {
		cfg.MaxPlanSteps = 6
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = 30 * time.Second
	}
	if cfg.TotalTimeout <= 0 {
		cfg.TotalTimeout = 180 * time.Second
	}
	return &Loop{cfg: cfg, llm: llm, executor: executor}
}

// Run executes the reasoning loop for the given task and returns a Result.
//
// Run always returns a Result — even when tools fail or the LLM hits its step
// limit the loop calls Reflect() to produce graceful degraded output.
func (l *Loop) Run(ctx context.Context, agentID, taskInput string) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, l.cfg.TotalTimeout)
	defer cancel()

	var steps []Step
	var reflectResp ReflectResponse

	strategy := l.cfg.Strategy
	if strategy == StrategyAuto {
		strategy = detectStrategy(taskInput)
	}

	switch strategy {
	case StrategyPlanExecute:
		steps, reflectResp = l.runPlanExecute(ctx, taskInput)
	default:
		steps, reflectResp = l.runReAct(ctx, taskInput)
	}

	return Result{
		Output:    reflectResp.Output,
		Steps:     steps,
		Confident: !containsToolErrors(steps),
		Duration:  time.Since(start),
	}
}

// ─── ReAct ────────────────────────────────────────────────────────────────────

func (l *Loop) runReAct(ctx context.Context, taskInput string) ([]Step, ReflectResponse) {
	var steps []Step

	for i := 0; i < l.cfg.MaxSteps; i++ {
		if ctx.Err() != nil {
			break
		}

		stepCtx, cancel := context.WithTimeout(ctx, l.cfg.StepTimeout)
		stepStart := time.Now()

		think, err := l.llm.Think(stepCtx, ThinkRequest{
			TaskInput:    taskInput,
			StepHistory:  steps,
			SystemPrompt: l.cfg.SystemPrompt,
			ToolNames:    l.cfg.ToolNames,
		})
		if err != nil {
			cancel()
			break // LLM error — reflect on partial trace
		}

		if think.IsDone {
			cancel()
			resp, _ := l.llm.Reflect(ctx, ReflectRequest{
				TaskInput:    taskInput,
				Steps:        steps,
				SystemPrompt: l.cfg.SystemPrompt,
				OutputFormat: l.cfg.OutputFormat,
			})
			if resp.Output == "" && think.FinalAnswer != "" {
				resp.Output = think.FinalAnswer
			}
			return steps, resp
		}

		// Execute the tool — failures are wrapped as observations, not panics.
		obs := l.executor.Execute(stepCtx, think.Action)
		obs = boundObservation(obs)

		steps = append(steps, Step{
			ID:       fmt.Sprintf("step-%d", i+1),
			Thought:  think.Thought,
			Action:   think.Action,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		cancel()
	}

	// MaxSteps exhausted or LLM errored — reflect on what we have.
	resp, _ := l.llm.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: l.cfg.SystemPrompt,
		OutputFormat: l.cfg.OutputFormat,
	})
	return steps, resp
}

// ─── Plan-Execute ─────────────────────────────────────────────────────────────

func (l *Loop) runPlanExecute(ctx context.Context, taskInput string) ([]Step, ReflectResponse) {
	plan, err := l.llm.Plan(ctx, l.cfg.SystemPrompt, taskInput, l.cfg.MaxPlanSteps)
	if err != nil {
		// Planning failed — fall back to ReAct.
		return l.runReAct(ctx, taskInput)
	}

	completedIDs := map[string]bool{}
	var steps []Step

	for _, ps := range plan.Steps {
		if ctx.Err() != nil {
			break
		}

		// Dependency ordering: skip steps whose dependencies haven't completed.
		// Because the plan is already ordered, an unmet dependency means an
		// upstream step failed — we skip the dependent step gracefully.
		depsOK := true
		for _, dep := range ps.DependsOn {
			if !completedIDs[dep] {
				depsOK = false
				break
			}
		}
		if !depsOK {
			steps = append(steps, Step{
				ID:      ps.ID,
				Thought: ps.Description,
				Obs: Observation{
					Content: fmt.Sprintf("skipped: dependency %v not completed", ps.DependsOn),
				},
			})
			continue
		}

		stepCtx, cancel := context.WithTimeout(ctx, l.cfg.StepTimeout)
		stepStart := time.Now()

		call := ToolCall{
			Tool:  ps.Tool,
			Input: map[string]string{"task": ps.Description},
		}
		obs := l.executor.Execute(stepCtx, call)
		obs = boundObservation(obs)

		steps = append(steps, Step{
			ID:       ps.ID,
			Thought:  ps.Description,
			Action:   call,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		completedIDs[ps.ID] = true
		cancel()
	}

	resp, _ := l.llm.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: l.cfg.SystemPrompt,
		OutputFormat: l.cfg.OutputFormat,
	})
	return steps, resp
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// detectStrategy inspects the task text and returns the most appropriate loop
// strategy. Called when StrategyAuto is configured so the user never needs to
// choose between react and plan_execute manually.
//
// Heuristic (intentionally simple — wrong classification is harmless; the
// worst case is running react on a planning task which still produces a
// good answer via Reflect):
//   - Planning keywords  → StrategyPlanExecute
//   - Research keywords  → StrategyReAct
//   - Everything else    → StrategyReAct (IsDone on step 1 = zero overhead)
func detectStrategy(taskInput string) LoopStrategy {
	t := strings.ToLower(taskInput)
	planningKeywords := []string{
		"plan", "planning", "strategy", "strategic", "roadmap",
		"decide", "decision", "should i", "pros and cons", "trade-off", "tradeoff",
		"compare", "comparison", "recommend", "recommendation",
		"prioritise", "prioritize", "milestone",
	}
	for _, kw := range planningKeywords {
		if strings.Contains(t, kw) {
			return StrategyPlanExecute
		}
	}
	// Default to ReAct — if the LLM answers in one step (IsDone=true, no tool
	// call), the loop returns immediately with zero extra overhead.
	return StrategyReAct
}

// boundObservation enforces the 8192-byte cap and wraps tool errors as content.
func boundObservation(obs Observation) Observation {
	if obs.Error != nil {
		obs.Content = fmt.Sprintf("tool error: %s", obs.Error.Error())
		obs.Error = obs.Error // preserve for Confident calculation
	}
	if len(obs.Content) > 8192 {
		obs.Content = obs.Content[:8192]
	}
	return obs
}

func containsToolErrors(steps []Step) bool {
	for _, s := range steps {
		if s.Obs.Error != nil || strings.HasPrefix(s.Obs.Content, "tool error:") {
			return true
		}
	}
	return false
}

// AvailableToolNames returns the tools this loop was configured with.
func (l *Loop) AvailableToolNames() []string {
	return l.cfg.ToolNames
}
