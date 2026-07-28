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
	"time"
)

// emitNodes announces each node of a freshly built draft, in declaration order,
// so a client can render the graph as it arrives. Structural endpoint blocks
// (trigger/exit) are included because the canvas draws them too — omitting them
// would make the streamed graph differ from the finished one.
func emitNodes(emit func(PipelineEvent), d Draft) {
	total := len(d.Flow.Nodes)
	for i, n := range d.Flow.Nodes {
		label := n.Description
		if strings.TrimSpace(label) == "" {
			label = n.Tool
		}
		if strings.TrimSpace(label) == "" {
			label = n.Agent
		}
		emit(PipelineEvent{
			Phase:   PhaseBuildGraph,
			Status:  StatusNode,
			Message: n.ID,
			Payload: map[string]any{
				"id": n.ID, "kind": n.Kind, "label": label,
				"index": i + 1, "total": total,
			},
		})
	}
}

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
	// StatusNode reports ONE constructed node inside build_graph, so the canvas
	// fills in progressively instead of appearing whole at the end. See the note
	// on emitNodes about what this does and does not claim.
	StatusNode PipelineStatus = "node"
	// StatusCancelled is the operator stopping the run. Distinct from an error:
	// nothing went wrong, and the partial draft is still worth keeping.
	StatusCancelled PipelineStatus = "cancelled"
)

// PipelineSource attributes an event to whoever actually did the work. ST-04
// requires the transcript to distinguish deterministic planner actions from LLM
// prompt refinement — without it a user cannot tell which parts of their
// workflow a model chose and which were decided by rules.
type PipelineSource string

const (
	SourcePlanner PipelineSource = "planner"
	SourceLLM     PipelineSource = "llm"
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
	// Source says WHO produced this step: the deterministic planner or the
	// model. The transcript previously rendered both identically, so a user
	// could not tell which decisions were rule-based and which were a model's.
	Source PipelineSource `json:"source,omitempty"`
	// ElapsedMS is milliseconds since the pipeline started. Without it the UI
	// had no way to show progress or spot a phase that had stalled, which is
	// what made a slow generate indistinguishable from a frozen one.
	ElapsedMS int64 `json:"elapsed_ms"`
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
	// PreferDeterministic pins graph design to the deterministic planner and
	// keeps the model out of the loop entirely.
	//
	// The default is model-first, because the deterministic planner cannot see
	// most of a workspace's capabilities and so cannot be relied on for accuracy.
	// This flag exists for the case where reproducibility genuinely matters more
	// — a regression suite, an air-gapped build, an audit that must show no model
	// touched the design. It is opt-in rather than the default so that choosing
	// reproducibility over correctness is a deliberate act.
	PreferDeterministic bool
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
// at each start/complete/skip boundary. The LLM is allowed to clarify/refine
// wording, but Soulacy owns strategy selection and graph construction.
func RunGeneratePipeline(ctx context.Context, llm LLM, intent string, catalog Catalog, opts PipelineOptions) (PipelineResult, error) {
	res := PipelineResult{}
	started := time.Now()
	emit := func(ev PipelineEvent) {
		ev.ElapsedMS = time.Since(started).Milliseconds()
		if ev.Source == "" {
			// Everything except prompt refinement is rule-based, so planner is the
			// correct default and an unlabelled event cannot silently imply a model
			// made the decision.
			ev.Source = SourcePlanner
		}
		res.PhaseLog = append(res.PhaseLog, ev)
		if opts.Emit != nil {
			opts.Emit(ev)
		}
	}

	// cancelled reports operator cancellation and records it in the transcript.
	// Checked between phases: the phases themselves are single synchronous calls,
	// so this bounds cancellation latency to one phase rather than pretending to
	// interrupt work mid-flight.
	cancelled := func(phase PipelineEventKind) bool {
		if ctx.Err() == nil {
			return false
		}
		emit(PipelineEvent{
			Phase: phase, Status: StatusCancelled,
			Message: "Cancelled — the partial draft is kept so nothing you had is lost.",
		})
		return true
	}

	// Phase 1 — clarify_intent (RefinePrompt). The ONLY phase a model touches.
	emit(PipelineEvent{Phase: PhaseClarifyIntent, Status: StatusStart, Message: "Clarifying intent", Source: SourceLLM})
	refineFn := RefinePrompt
	if opts.Light {
		refineFn = LightRefinePrompt
	}
	refinement, err := refineFn(ctx, llm, intent, catalog)
	if err != nil {
		emit(PipelineEvent{Phase: PhaseClarifyIntent, Status: StatusError, Message: err.Error(), Source: SourceLLM})
		return res, fmt.Errorf("clarify_intent: %w", err)
	}
	res.Refinement = refinement
	if cancelled(PhaseClarifyIntent) {
		return res, ctx.Err()
	}
	emit(PipelineEvent{
		Phase:   PhaseClarifyIntent,
		Status:  StatusComplete,
		Source:  SourceLLM,
		Message: refinementSummary(refinement),
		Payload: map[string]any{
			"refined_intent": refinement.RefinedIntent,
			"summary":        refinement.Summary,
			"assumptions":    refinement.Assumptions,
			"questions":      refinement.Questions,
		},
	})

	// Phase 2 — choose_strategy (deterministic Strategy Advisor over the refined text).
	emit(PipelineEvent{Phase: PhaseChooseStrategy, Status: StatusStart, Message: "Choosing execution strategy"})
	combined := strings.TrimSpace(refinement.RefinedIntent + " " + intent)
	advice := AdviseStrategy(combined, catalog, refinement.RecommendedMode, false)
	strategy := advice.RuntimeStrategy
	res.Strategy = strategy
	strategyMsg := "workflow (fixed graph)"
	if advice.Mode != "workflow" {
		strategyMsg = advice.Mode + " (reasoning agent)"
	}
	emit(PipelineEvent{
		Phase:   PhaseChooseStrategy,
		Status:  StatusComplete,
		Message: "Strategy: " + strategyMsg,
		Payload: map[string]any{"strategy": strategy, "mode": advice.Mode, "reason": advice.Reason, "pattern": advice.DeterministicPattern},
	})

	// Phase 3 — build_graph.
	//
	// The MODEL designs the graph, grounded in the whole catalogue, and the
	// deterministic planner is the safety net rather than the default.
	//
	// It used to be the other way round: a keyword pattern match won outright and
	// the model never saw the intent. That is reproducible but blind — the
	// workflow skeletons are hardcoded to web_search and never reference MCP, and
	// neither deterministic path selects skills at all. So a prompt naming an
	// installed MCP tool produced a graph that used none of it. Reproducibly
	// wrong is still wrong.
	//
	// Ordering the two this way keeps what the deterministic planner is actually
	// good for: it still catches the case where the model errors, and — because
	// the model's graph is CONTRACT-CHECKED before it is accepted — the case
	// where a weak builder model produces something that does not hold together.
	// Accuracy first, with a floor under it.
	emit(PipelineEvent{Phase: PhaseBuildGraph, Status: StatusStart, Message: "Designing the graph"})
	// Prefer the refined intent for the compile step so the model sees the
	// operator-visible version.
	compileIntent := combined
	if strings.TrimSpace(refinement.RefinedIntent) != "" {
		compileIntent = refinement.RefinedIntent
	}

	deterministic := func() (Result, bool) {
		if advice.Mode == "workflow" {
			return CompileDeterministicWorkflow(compileIntent, catalog, opts.Answers)
		}
		return CompileDeterministicAgent(compileIntent, catalog, strategy, opts.Answers)
	}

	var compileRes Result
	var ok bool
	designedBy := SourceLLM

	if llm != nil && !opts.PreferDeterministic {
		emit(PipelineEvent{
			Phase: PhaseBuildGraph, Status: StatusStart, Source: SourceLLM,
			Message: "The builder model is choosing tools, skills and MCP servers from what is installed.",
		})
		var lerr error
		if advice.Mode == "workflow" {
			compileRes, lerr = Compile(ctx, llm, compileIntent, catalog, opts.Answers)
		} else {
			compileRes, lerr = CompileAgent(ctx, llm, compileIntent, catalog, strategy, opts.Answers)
		}
		ok = lerr == nil
		if ok {
			// Only accept the model's graph if it actually holds together. This is
			// what makes model-first safe on a small local builder: a graph that
			// fails its contract is not silently preferred over a working
			// deterministic one.
			if c := AssessContract(compileRes.Workflow, catalog, opts.In); c.Blockers > 0 {
				ok = false
				lerr = fmt.Errorf("the model's graph did not pass its contract (%d blocker(s))", c.Blockers)
			}
		}
		if !ok {
			emit(PipelineEvent{
				Phase: PhaseBuildGraph, Status: StatusSkip, Source: SourceLLM,
				Message: "Falling back to the deterministic planner: " + lerr.Error(),
			})
		}
	}

	if !ok {
		designedBy = SourcePlanner
		compileRes, ok = deterministic()
	}

	if !ok {
		err := fmt.Errorf("neither the builder model nor the deterministic planner could build a %s draft for this intent", advice.Mode)
		emit(PipelineEvent{Phase: PhaseBuildGraph, Status: StatusError, Message: err.Error()})
		return res, fmt.Errorf("build_graph: %w", err)
	}

	// Say WHO designed the graph. The deterministic path advertises "no LLM
	// designed the graph"; when that stops being true the user has to be told, or
	// the guarantee silently becomes a lie.
	if designedBy == SourceLLM {
		compileRes.Notes = append(compileRes.Notes,
			"The builder model designed this graph from the installed tools, skills and MCP servers.")
	} else {
		// A deterministic graph can still miss what the user asked for; say so
		// rather than presenting it as a complete answer.
		if short := DeterministicShortfall(compileIntent, catalog, compileRes); short != "" {
			compileRes.Notes = append(compileRes.Notes,
				"Heads up: this graph was built by the deterministic planner and "+short+". Review the steps before saving.")
		}
	}
	if compileRes.Generation != nil {
		compileRes.Generation.PlanMatched = len(compileRes.Plan) > 0
		compileRes.Generation.PatternMatched = advice.DeterministicPattern != "" || len(MatchPatterns(compileIntent, catalog, 1)) > 0
	}
	res.Compile = compileRes

	// Announce each constructed node so the canvas fills in progressively rather
	// than appearing whole at the end.
	//
	// Honest about the mechanism: the deterministic planner is ONE synchronous
	// pass, so these are emitted after it returns, not from inside it. That is
	// still strictly more information than the old aggregate count — the user
	// sees which steps exist, in order, and what each one is — and it is emitted
	// at full speed with no artificial pacing. Faking a delay to look like
	// incremental construction would be theatre, and would make a fast generate
	// slower for no reason. Emitting from inside the planner needs a callback
	// threaded through CompileDeterministicWorkflow/Agent; that is the real fix
	// and is noted in the handoff.
	emitNodes(emit, compileRes.Workflow)

	if cancelled(PhaseBuildGraph) {
		// The draft is already on res.Compile, so a cancel here keeps the graph
		// that was built instead of discarding the work.
		return res, ctx.Err()
	}
	emit(PipelineEvent{
		Phase:   PhaseBuildGraph,
		Status:  StatusComplete,
		Message: buildGraphSummary(compileRes.Workflow),
		Payload: map[string]any{
			"nodes":              len(compileRes.Workflow.Flow.Nodes),
			"edges":              len(compileRes.Workflow.Flow.Edges),
			"tools":              len(compileRes.Workflow.Tools),
			"questions":          compileRes.Questions,
			"deterministic_plan": len(compileRes.Notes) > 0 && strings.Contains(strings.Join(compileRes.Notes, "\n"), "deterministic planner"),
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
		RepairContractStructure(&compileRes.Workflow, compileIntent, catalog, opts.Answers, contract)
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
		return fmt.Sprintf("%s agent draft (%d tools, %d peers)", strings.TrimSpace(d.Strategy), len(d.Tools), len(d.NewAgents))
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
