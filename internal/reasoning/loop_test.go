package reasoning_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/reasoning"
)

// ─── Stub backends ───────────────────────────────────────────────────────────

// stubLLM is a controllable LLMBackend for testing.
type stubLLM struct {
	thinkCalls int
	doneOnStep int // Think returns IsDone=true on this call number
	planSteps  []reasoning.PlannedStep
	reflectOut string
}

func (s *stubLLM) Think(_ context.Context, req reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	s.thinkCalls++
	if s.thinkCalls >= s.doneOnStep {
		return reasoning.ThinkResponse{IsDone: true, Thought: "done", FinalAnswer: "task complete"}, nil
	}
	return reasoning.ThinkResponse{
		Thought: "thinking",
		IsDone:  false,
		Action:  reasoning.ToolCall{Tool: "web_search", Input: map[string]string{"query": "test"}},
	}, nil
}

func (s *stubLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{Goal: "test goal", Steps: s.planSteps}, nil
}

func (s *stubLLM) Reflect(_ context.Context, req reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	out := s.reflectOut
	if out == "" {
		out = "synthesised answer"
	}
	return reasoning.ReflectResponse{Output: out}, nil
}

// stubExecutor always succeeds with a canned observation.
type stubExecutor struct{}

func (s *stubExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	return reasoning.Observation{Content: "ok: " + call.Tool, Source: call.Tool}
}

// errorExecutor always returns an error (Scenario D).
type errorExecutor struct{}

func (e *errorExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	return reasoning.Observation{Error: errors.New("injected tool failure")}
}

type scriptedLLM struct {
	responses      []reasoning.ThinkResponse
	thinkCalls     int
	reflectOut     string
	reflectOutputs []string
	reflectCalls   int
}

func (s *scriptedLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	s.thinkCalls++
	if s.thinkCalls <= len(s.responses) {
		return s.responses[s.thinkCalls-1], nil
	}
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "done"}, nil
}

func (s *scriptedLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (s *scriptedLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	s.reflectCalls++
	if s.reflectCalls <= len(s.reflectOutputs) {
		return reasoning.ReflectResponse{Output: s.reflectOutputs[s.reflectCalls-1]}, nil
	}
	out := s.reflectOut
	if out == "" {
		out = "done"
	}
	return reasoning.ReflectResponse{Output: out}, nil
}

type flakyThinkLLM struct {
	thinkCalls int
}

func (f *flakyThinkLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	f.thinkCalls++
	if f.thinkCalls == 1 {
		return reasoning.ThinkResponse{}, errors.New("invalid JSON reasoning response")
	}
	if f.thinkCalls == 2 {
		return reasoning.ThinkResponse{
			IsDone: false,
			Action: reasoning.ToolCall{
				Tool:  "fetch_url",
				Input: map[string]string{"url": "https://example.com"},
			},
		}, nil
	}
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "fetched"}, nil
}

func (f *flakyThinkLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (f *flakyThinkLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{Output: "done"}, nil
}

type recordingExecutor struct {
	calls []reasoning.ToolCall
}

func (r *recordingExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	r.calls = append(r.calls, call)
	return reasoning.Observation{Content: "ok", Source: call.Tool}
}

type htmlExecutor struct{}

func (h *htmlExecutor) Execute(_ context.Context, call reasoning.ToolCall) reasoning.Observation {
	return reasoning.Observation{
		Source: call.Tool,
		Content: "URL: https://example.com\nStatus: 200\nContent-Type: text/html; charset=UTF-8\n\n" +
			`<!doctype html><html><head><title>Example Article</title><script>huge()</script></head><body><h1>Example Article</h1><p>Useful article text.</p></body></html>`,
	}
}

type htmlHistoryLLM struct {
	thinkCalls int
	sawRawHTML bool
	sawText    bool
}

func (h *htmlHistoryLLM) Think(_ context.Context, req reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	h.thinkCalls++
	if h.thinkCalls == 1 {
		return reasoning.ThinkResponse{
			IsDone: false,
			Action: reasoning.ToolCall{
				Tool:  "fetch_url",
				Input: map[string]string{"url": "https://example.com"},
			},
		}, nil
	}
	if len(req.StepHistory) > 0 {
		obs := req.StepHistory[0].Obs.Content
		h.sawRawHTML = strings.Contains(strings.ToLower(obs), "<html") || strings.Contains(strings.ToLower(obs), "<script")
		h.sawText = strings.Contains(obs, "HTML fetched; readable text excerpt") && strings.Contains(obs, "Useful article text.")
	}
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "done"}, nil
}

func (h *htmlHistoryLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (h *htmlHistoryLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{Output: "done"}, nil
}

type alwaysBadThinkLLM struct {
	thinkCalls int
}

func (a *alwaysBadThinkLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	a.thinkCalls++
	return reasoning.ThinkResponse{}, errors.New("unexpected end of JSON input")
}

func (a *alwaysBadThinkLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (a *alwaysBadThinkLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{Output: "should not need reflect"}, nil
}

type interleavedBadThinkLLM struct {
	thinkCalls   int
	reflectCalls int
}

func (i *interleavedBadThinkLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	i.thinkCalls++
	if i.thinkCalls == 1 {
		return reasoning.ThinkResponse{
			Thought: "get queue",
			IsDone:  false,
			Action:  reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"queue": "pending_resources"}},
		}, nil
	}
	if i.thinkCalls == 5 || i.thinkCalls == 9 {
		return reasoning.ThinkResponse{
			Thought: "refresh",
			IsDone:  false,
			Action:  reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"queue": "pending_resources"}},
		}, nil
	}
	return reasoning.ThinkResponse{}, errors.New("invalid reasoning JSON")
}

func (i *interleavedBadThinkLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{}, errors.New("plan not scripted")
}

func (i *interleavedBadThinkLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	i.reflectCalls++
	return reasoning.ReflectResponse{Output: "Recovered from the last useful queue observation."}, nil
}

type fallbackPlanLLM struct {
	thinkCalls int
}

func (f *fallbackPlanLLM) Think(_ context.Context, _ reasoning.ThinkRequest) (reasoning.ThinkResponse, error) {
	f.thinkCalls++
	if f.thinkCalls == 1 {
		return reasoning.ThinkResponse{
			Thought: "use available tool",
			IsDone:  false,
			Action:  reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"queue": "pending_resources"}},
		}, nil
	}
	return reasoning.ThinkResponse{IsDone: true, FinalAnswer: "done"}, nil
}

func (f *fallbackPlanLLM) Plan(_ context.Context, _, _ string, _ int) (reasoning.Plan, error) {
	return reasoning.Plan{Goal: "bad plan", Steps: []reasoning.PlannedStep{
		{ID: "step-1", Description: "run a tool that is not installed", Tool: "missing_tool"},
	}}, nil
}

func (f *fallbackPlanLLM) Reflect(_ context.Context, _ reasoning.ReflectRequest) (reasoning.ReflectResponse, error) {
	return reasoning.ReflectResponse{Output: "done"}, nil
}

// ─── Scenario A — ReAct loop ─────────────────────────────────────────────────

// Scenario A: stub LLM returns IsDone=true on step 2.
// Assert Result.Output non-empty, Steps has exactly 1 entry (the step before done),
// Result.Duration > 0.
func TestScenarioA_ReActLoop(t *testing.T) {
	llm := &stubLLM{doneOnStep: 2} // IsDone=true on second Think call
	exec := &stubExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     4,
		StepTimeout:  5 * time.Second,
		TotalTimeout: 30 * time.Second,
	}, llm, exec)

	result := loop.Run(context.Background(), "research-agent", "What is Soulacy?")

	if result.Output == "" {
		t.Error("expected non-empty Output")
	}
	// Step 1 executes (Think call 1 returns IsDone=false → tool runs).
	// Think call 2 returns IsDone=true → Reflect is called immediately with 1 step.
	if len(result.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(result.Steps))
	}
	if result.Duration <= 0 {
		t.Error("expected Duration > 0")
	}
}

func TestReActRecoversTextualToolCallFinalAnswer(t *testing.T) {
	llm := &scriptedLLM{responses: []reasoning.ThinkResponse{
		{
			IsDone:      true,
			Thought:     "queue the URL",
			FinalAnswer: `queue_put({"name":"pending_resources","item":{"id":"abc","type":"url","content":"https://example.com"}})`,
		},
		{IsDone: true, FinalAnswer: "queued"},
	}}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_put"},
	}, llm, exec)

	result := loop.Run(context.Background(), "research-librarian", "add this url https://example.com")

	if len(exec.calls) != 1 {
		t.Fatalf("expected recovered queue_put call, got %d", len(exec.calls))
	}
	if exec.calls[0].Tool != "queue_put" {
		t.Fatalf("tool = %q, want queue_put", exec.calls[0].Tool)
	}
	item, ok := exec.calls[0].Arguments["item"].(map[string]any)
	if !ok || item["content"] != "https://example.com" {
		t.Fatalf("nested item was not preserved: %#v", exec.calls[0].Arguments["item"])
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(result.Steps))
	}
}

func TestReActDoesNotStopOnProgressNoteFinalAnswer(t *testing.T) {
	llm := &scriptedLLM{responses: []reasoning.ThinkResponse{
		{IsDone: true, Thought: "auth complete", FinalAnswer: "Starting daily processing. Proceeding to Step 1: checking for pending resources."},
		{IsDone: false, Thought: "check queue", Action: reasoning.ToolCall{Tool: "queue_names"}},
		{IsDone: true, FinalAnswer: "No pending resources."},
	}}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     4,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_names"},
	}, llm, exec)

	result := loop.Run(context.Background(), "research-librarian", "__trigger:manual__")

	if len(exec.calls) != 1 || exec.calls[0].Tool != "queue_names" {
		t.Fatalf("expected loop to continue into queue_names, calls=%#v", exec.calls)
	}
	if len(result.Steps) < 2 {
		t.Fatalf("expected controller step plus tool step, got %d", len(result.Steps))
	}
}

func TestReActDoesNotStopOnProgressNoteReflectOutput(t *testing.T) {
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{IsDone: false, Thought: "check queues", Action: reasoning.ToolCall{Tool: "queue_names"}},
			{IsDone: true, Thought: "queue exists", FinalAnswer: "ready to process"},
			{IsDone: false, Thought: "list queue", Action: reasoning.ToolCall{Tool: "queue_list", Input: map[string]string{"name": "pending_resources"}}},
			{IsDone: true, FinalAnswer: "processed"},
		},
		reflectOut: "Daily processing triggered. The pending_resources queue exists with 1 item. Proceeding to list and process pending items.",
	}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     5,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_names", "queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "research-librarian", "__trigger:manual__")

	if len(exec.calls) < 2 {
		t.Fatalf("expected loop to reject progress-note reflection and continue, calls=%#v", exec.calls)
	}
	if exec.calls[0].Tool != "queue_names" || exec.calls[1].Tool != "queue_list" {
		t.Fatalf("unexpected calls: %#v", exec.calls)
	}
	if len(result.Steps) < 3 {
		t.Fatalf("expected queue_names, controller note, and queue_list steps, got %d", len(result.Steps))
	}
}

func TestReActContinuesAfterThinkError(t *testing.T) {
	llm := &flakyThinkLLM{}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     4,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"fetch_url"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "fetch https://example.com")

	if len(exec.calls) != 1 || exec.calls[0].Tool != "fetch_url" {
		t.Fatalf("expected loop to continue after Think error and call fetch_url, calls=%#v", exec.calls)
	}
	if len(result.Steps) < 2 {
		t.Fatalf("expected controller note plus recovered tool step, got %d", len(result.Steps))
	}
}

func TestReActRecoversLegacyActionFromReflectOutput(t *testing.T) {
	llm := &scriptedLLM{
		responses: []reasoning.ThinkResponse{
			{IsDone: true, Thought: "need to fetch", FinalAnswer: "ready"},
			{IsDone: true, FinalAnswer: "done"},
		},
		reflectOutputs: []string{
			"I need to fetch the URL content first.\nAction: fetch_url(map[url:https://example.com/article])",
			"fetched",
		},
	}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     4,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"fetch_url"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "process queued resource")

	if len(exec.calls) != 1 {
		t.Fatalf("expected recovered legacy fetch_url call, got %d", len(exec.calls))
	}
	if exec.calls[0].Tool != "fetch_url" || exec.calls[0].Input["url"] != "https://example.com/article" {
		t.Fatalf("unexpected recovered call: %#v", exec.calls[0])
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 recovered tool step", len(result.Steps))
	}
}

func TestReActCompactsHTMLObservationBeforeNextThink(t *testing.T) {
	llm := &htmlHistoryLLM{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"fetch_url"},
	}, llm, &htmlExecutor{})

	_ = loop.Run(context.Background(), "any-react-agent", "fetch https://example.com")

	if llm.sawRawHTML {
		t.Fatalf("expected raw HTML/script tags to be compacted before the next Think")
	}
	if !llm.sawText {
		t.Fatalf("expected readable HTML text excerpt in step history")
	}
}

func TestReActStopsAfterRepeatedThinkErrors(t *testing.T) {
	llm := &alwaysBadThinkLLM{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     30,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
	}, llm, &recordingExecutor{})

	result := loop.Run(context.Background(), "any-react-agent", "do work")

	if llm.thinkCalls != 4 {
		t.Fatalf("think calls = %d, want 4 before controller stop", llm.thinkCalls)
	}
	if len(result.Steps) != 4 {
		t.Fatalf("steps = %d, want 4 controller error steps", len(result.Steps))
	}
	if !strings.Contains(result.Output, "invalid ReAct JSON too many times") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestReActReflectsAfterRepeatedThinkErrorsWithUsefulObservation(t *testing.T) {
	llm := &interleavedBadThinkLLM{}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     30,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-react-agent", "process pending queue")

	if llm.thinkCalls != 4 {
		t.Fatalf("think calls = %d, want 4 before reflective recovery", llm.thinkCalls)
	}
	if llm.reflectCalls != 1 {
		t.Fatalf("reflect calls = %d, want 1 recovery reflection", llm.reflectCalls)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("tool calls = %d, want the initial valid call only", len(exec.calls))
	}
	if !strings.Contains(result.Output, "Recovered from the last useful queue observation") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

// ─── Scenario B — Plan-Execute ───────────────────────────────────────────────

// Scenario B: 3-step plan; assert Steps has 3 entries, dependency ordering respected.
func TestScenarioB_PlanExecute(t *testing.T) {
	planSteps := []reasoning.PlannedStep{
		{ID: "step-1", Description: "gather data", Tool: "web_search", DependsOn: []string{}},
		{ID: "step-2", Description: "analyse data", Tool: "web_search", DependsOn: []string{"step-1"}},
		{ID: "step-3", Description: "write report", Tool: "web_search", DependsOn: []string{"step-2"}},
	}

	llm := &stubLLM{planSteps: planSteps}
	exec := &stubExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxPlanSteps: 6,
		StepTimeout:  5 * time.Second,
		TotalTimeout: 30 * time.Second,
	}, llm, exec)

	result := loop.Run(context.Background(), "decision-agent", "Produce a research report")

	if len(result.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(result.Steps))
	}

	// Verify step IDs match the plan.
	ids := map[string]int{}
	for i, s := range result.Steps {
		ids[s.ID] = i
	}
	if ids["step-1"] >= ids["step-2"] {
		t.Error("step-1 must complete before step-2")
	}
	if ids["step-2"] >= ids["step-3"] {
		t.Error("step-2 must complete before step-3")
	}
}

func TestPlanExecuteFallsBackToReActForUnavailablePlanTool(t *testing.T) {
	llm := &fallbackPlanLLM{}
	exec := &recordingExecutor{}
	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyPlanExecute,
		MaxSteps:     3,
		MaxPlanSteps: 3,
		StepTimeout:  time.Second,
		TotalTimeout: 5 * time.Second,
		ToolNames:    []string{"queue_list"},
	}, llm, exec)

	result := loop.Run(context.Background(), "any-agent", "process queue")

	if len(exec.calls) != 1 || exec.calls[0].Tool != "queue_list" {
		t.Fatalf("expected fallback ReAct queue_list call, calls=%#v", exec.calls)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("steps = %d, want one ReAct step", len(result.Steps))
	}
}

// ─── Scenario D — Tool failure resilience ────────────────────────────────────

// Scenario D: errorExecutor always errors; loop must not panic, each step
// records an error, and Run still returns a Result with non-empty Output.
func TestScenarioD_ToolFailureResilience(t *testing.T) {
	llm := &stubLLM{doneOnStep: 99} // never done on its own → exhaust MaxSteps
	exec := &errorExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     3,
		StepTimeout:  5 * time.Second,
		TotalTimeout: 30 * time.Second,
	}, llm, exec)

	// Must not panic.
	result := loop.Run(context.Background(), "research-agent", "failing task")

	// All steps should record a tool error.
	if len(result.Steps) != 3 {
		t.Errorf("expected 3 steps (MaxSteps), got %d", len(result.Steps))
	}
	for i, s := range result.Steps {
		if s.Obs.Error == nil && s.Obs.Content == "" {
			t.Errorf("step %d: expected error observation, got empty", i)
		}
	}

	// Result must still have output from Reflect() (graceful degradation).
	if result.Output == "" {
		t.Error("expected non-empty Output even after all tool failures")
	}

	// Confident must be false when tools errored.
	if result.Confident {
		t.Error("expected Confident=false when all tools errored")
	}
}

// ─── Misc unit tests ─────────────────────────────────────────────────────────

// TestLoop_AvailableToolNames: LoopConfig.ToolNames is exposed via AvailableToolNames().
func TestLoop_AvailableToolNames(t *testing.T) {
	llm := &stubLLM{doneOnStep: 1}
	exec := &stubExecutor{}
	tools := []string{"web_search", "memory_read", "memory_write"}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:  reasoning.StrategyReAct,
		ToolNames: tools,
	}, llm, exec)

	got := loop.AvailableToolNames()
	if len(got) != len(tools) {
		t.Errorf("expected %d tools, got %d", len(tools), len(got))
	}
}

// TestLoop_TotalTimeoutRespected: a very short TotalTimeout must cause the loop
// to return before MaxSteps is exhausted.
func TestLoop_TotalTimeoutRespected(t *testing.T) {
	llm := &stubLLM{doneOnStep: 999} // never done — relies on timeout
	exec := &stubExecutor{}

	loop := reasoning.New(reasoning.LoopConfig{
		Strategy:     reasoning.StrategyReAct,
		MaxSteps:     100,
		StepTimeout:  500 * time.Millisecond,
		TotalTimeout: 50 * time.Millisecond, // very short
	}, llm, exec)

	start := time.Now()
	result := loop.Run(context.Background(), "test-agent", "long running task")
	elapsed := time.Since(start)

	// Should complete well within 1 second despite MaxSteps=100.
	if elapsed > 2*time.Second {
		t.Errorf("expected loop to respect TotalTimeout, took %v", elapsed)
	}
	// Result is still returned (graceful degradation).
	if result.Duration <= 0 {
		t.Error("expected Duration > 0")
	}
}
