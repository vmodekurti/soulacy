package studio

// generatepipeline.go — Story 9 M (Cohort C). The Studio generate flow used
// to be a single opaque click that ran refine → compile → validate → repair
// inside two sequential POSTs, with intermediate phases invisible to the
// operator. This file exposes the pipeline as 5 discrete phases so the
// gateway can stream each boundary as an SSE event: `clarify_intent →
// choose_strategy → build_graph → validate → repair`.
//
// The orchestrator is single-shot from the client's point of view (streamed
// mode) but the GUI can also buffer the events and reveal them one at a time
// on Continue (wizard mode). No separate wizard endpoints — the same code
// path serves both surfaces.

import (
	"context"
	"fmt"
	"strings"
)

// PipelineEventKind is the discrete phase identifier used on the wire.
type PipelineEventKind string

const (
	PhaseClarifyIntent  PipelineEventKind = "clarify_intent"
	PhaseChooseStrategy PipelineEventKind = "choose_strategy"
	PhaseBuildGraph     PipelineEventKind = "build_graph"
	PhaseValidate       PipelineEventKind = "validate"
	PhaseRepair         PipelineEventKind = "repair"
)

// PipelineStatus is the position of the event inside a phase.
type PipelineStatus string

const (
	StatusStart    PipelineStatus = "start"
	StatusComplete PipelineStatus = "complete"
	StatusSkip     PipelineStatus = "skip"
	StatusError    PipelineStatus = "error"
)

// PipelineEvent is the on-wire shape emitted between phases so the GUI can
// render a live-transcript row or a wizard step card. Payload carries the
// per-phase intermediate output the operator would want to inspect: the
// refined intent, the chosen strategy, the graph summary, the contract
// verdict, the repair changes. Kept flat so the SSE JSON stays operator-
// readable when tailed with curl.
type PipelineEvent struct {
	Phase   PipelineEventKind `json:"phase"`
	Status  PipelineStatus    `json:"status"`
	Message string            `json:"message,omitempty"`
	Payload map[string]any    `json:"payload,omitempty"`
}

// PipelineOptions bundles the knobs a caller can pass. Emit is the SSE
// sink — a nil Emit turns this into a synchronous non-streaming pipeline
// (useful for tests).
type PipelineOptions struct {
	Answers    map[string]string
	Light      bool // use LightRefinePrompt for a touched-up re-generate
	In         PreflightInput
	Emit       func(PipelineEvent)
	SkipRepair bool
	AutoRepair bool // when true, apply deterministic repairs; false = report only
}

// PipelineResult mirrors compile.Result but is enriched with the phase
// outputs so the final `done` event carries everything the operator saw
// streaming plus the final drafted workflow.
type PipelineResult struct {
	Refinement PromptRefinement `json:"refinement"`
	Strategy   string           `json:"strategy"`
	Compile    Result           `json:"compile"`
	Contract   ContractResult   `json:"contract"`
	Preflight  PreflightResult  `json:"preflight"`
	Repaired   bool             `json:"repaired,omitempty"`
	// PhaseLog is the ordered list of events emitted during the run — kept
	// so a caller that used the sync form still gets the transcript.
	PhaseLog []PipelineEvent `json:"phase_log,omitempty"`
}

// RunGeneratePipeline orchestrates the 5 phases and emits one PipelineEvent
// at each start/complete/skip boundary. It reuses the existing single-shot
// primitives (RefinePrompt, RecommendAgentMode, Compile / CompileAgent,
// Preflight, AssessContract, RepairWiring) rather than rewriting any of
// them, so behaviour matches the classic sequential entry points.
func RunGeneratePipeline(ctx context.Context, llm LLM, intent string, catalog Catalog, opts PipelineOptions) (PipelineResult, error) {
	res := PipelineResult{}
	emit := func(ev PipelineEvent) {
		res.PhaseLog = append(res.PhaseLog, ev)
		if opts.Emit != nil {
			opts.Emit(ev)
		}
	}

	// Phase 1 — clarify_intent (RefinePrompt).
	emit(PipelineEvent{Phase: PhaseClarifyIntent, Status: StatusStart, Message: "Clarifying intent"})
	refineFn := RefinePrompt
	if opts.Light {
		refineFn = LightRefinePrompt
	}
	refinement, err := refineFn(ctx, llm, intent, catalog)
	if err != nil {
		emit(PipelineEvent{Phase: PhaseClarifyIntent, Status: StatusError, Message: err.Error()})
		return res, fmt.Errorf("clarify_intent: %w", err)
	}
	res.Refinement = refinement
	emit(PipelineEvent{
		Phase:   PhaseClarifyIntent,
		Status:  StatusComplete,
		Message: refinementSummary(refinement),
		Payload: map[string]any{
			"refined_intent": refinement.RefinedIntent,
			"summary":        refinement.Summary,
			"assumptions":    refinement.Assumptions,
			"questions":      refinement.Questions,
		},
	})

	// Phase 2 — choose_strategy (RecommendAgentMode over the refined text).
	emit(PipelineEvent{Phase: PhaseChooseStrategy, Status: StatusStart, Message: "Choosing execution strategy"})
	combined := strings.TrimSpace(refinement.RefinedIntent + " " + intent)
	strategy := refinement.RecommendedMode
	if strings.TrimSpace(strategy) == "" {
		if s := RecommendAgentMode(combined); s != "" {
			strategy = s
		}
	}
	res.Strategy = strategy
	strategyMsg := "workflow (fixed graph)"
	if strategy != "" {
		strategyMsg = strategy + " (reasoning agent)"
	}
	emit(PipelineEvent{
		Phase:   PhaseChooseStrategy,
		Status:  StatusComplete,
		Message: "Strategy: " + strategyMsg,
		Payload: map[string]any{"strategy": strategy, "reason": refinement.ModeReason},
	})

	// Phase 3 — build_graph (Compile / CompileAgent).
	emit(PipelineEvent{Phase: PhaseBuildGraph, Status: StatusStart, Message: "Building the draft"})
	// Prefer the refined intent for the compile step so the model sees the
	// operator-visible version.
	compileIntent := combined
	if strings.TrimSpace(refinement.RefinedIntent) != "" {
		compileIntent = refinement.RefinedIntent
	}
	var (
		compileRes Result
		compileErr error
	)
	if isAgentStrategy(strategy) {
		compileRes, compileErr = CompileAgent(ctx, llm, compileIntent, catalog, strategy, opts.Answers)
	} else {
		compileRes, compileErr = Compile(ctx, llm, compileIntent, catalog, opts.Answers)
	}
	if compileErr != nil {
		emit(PipelineEvent{Phase: PhaseBuildGraph, Status: StatusError, Message: compileErr.Error()})
		return res, fmt.Errorf("build_graph: %w", compileErr)
	}
	res.Compile = compileRes
	emit(PipelineEvent{
		Phase:   PhaseBuildGraph,
		Status:  StatusComplete,
		Message: buildGraphSummary(compileRes.Workflow),
		Payload: map[string]any{
			"nodes":     len(compileRes.Workflow.Flow.Nodes),
			"edges":     len(compileRes.Workflow.Flow.Edges),
			"questions": compileRes.Questions,
		},
	})

	// Phase 4 — validate (Preflight + AssessContract).
	emit(PipelineEvent{Phase: PhaseValidate, Status: StatusStart, Message: "Validating the draft"})
	pf := Preflight(compileRes.Workflow, opts.In)
	contract := AssessContract(compileRes.Workflow, catalog, opts.In)
	res.Preflight = pf
	res.Contract = contract
	emit(PipelineEvent{
		Phase:   PhaseValidate,
		Status:  StatusComplete,
		Message: contract.Summary,
		Payload: map[string]any{
			"score":     contract.Score,
			"blockers":  contract.Blockers,
			"warnings":  contract.Warnings,
			"preflight": pf.OK,
		},
	})

	// Phase 5 — repair. Deterministic-only in this pipeline (LLM repair
	// stays in BuildUntilWorks, which the "Build until it works" button
	// still owns). This phase either applies a wiring pass or reports that
	// nothing repairable was found.
	if opts.SkipRepair || contract.OK {
		emit(PipelineEvent{Phase: PhaseRepair, Status: StatusSkip, Message: "No repair needed"})
	} else if opts.AutoRepair {
		before := countIssues(pf, contract)
		RepairWiring(&compileRes.Workflow, catalog)
		pf2 := Preflight(compileRes.Workflow, opts.In)
		contract2 := AssessContract(compileRes.Workflow, catalog, opts.In)
		after := countIssues(pf2, contract2)
		res.Compile = compileRes
		res.Preflight = pf2
		res.Contract = contract2
		res.Repaired = after < before
		msg := "Applied deterministic wiring repair"
		if res.Repaired {
			msg = fmt.Sprintf("Applied wiring repair — %d issue(s) fixed", before-after)
		} else {
			msg = "Wiring repair pass found nothing to change"
		}
		emit(PipelineEvent{
			Phase:   PhaseRepair,
			Status:  StatusComplete,
			Message: msg,
			Payload: map[string]any{"issues_before": before, "issues_after": after},
		})
	} else {
		emit(PipelineEvent{
			Phase:   PhaseRepair,
			Status:  StatusSkip,
			Message: "Repair available but auto-repair is off — use Build until it works",
			Payload: map[string]any{"blockers": contract.Blockers, "warnings": contract.Warnings},
		})
	}
	return res, nil
}

func refinementSummary(r PromptRefinement) string {
	sum := strings.TrimSpace(r.Summary)
	if sum != "" {
		return sum
	}
	if q := len(r.Questions); q > 0 {
		return fmt.Sprintf("Clarified with %d question(s)", q)
	}
	return "Intent refined"
}

func buildGraphSummary(d Draft) string {
	if d.IsAgent() {
		return fmt.Sprintf("Reasoning agent draft (%d tools, %d peers)", len(d.Tools), len(d.NewAgents))
	}
	return fmt.Sprintf("Workflow draft (%d nodes, %d edges)", len(d.Flow.Nodes), len(d.Flow.Edges))
}

func countIssues(pf PreflightResult, c ContractResult) int {
	n := c.Blockers + c.Warnings
	if !pf.OK {
		n += len(pf.Blockers)
	}
	return n
}
