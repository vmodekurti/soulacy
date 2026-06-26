// buildloop.go — the autonomous build-verify-repair orchestrator (Architect
// pillar #1): the engine that makes a generated draft actually work, at any cost.
//
// Everything Studio learned to do in isolation — ground, validate against the
// live environment, deep-introspect tool calls, deterministically auto-wire,
// LLM-repair, and execute — is assembled here into a single self-driving loop
// with an attempt budget and a full, transparent transcript:
//
//	repeat up to MaxAttempts:
//	  1. deterministic repair (auto-wire args, reconcile vars, fix template typos)
//	  2. preflight against the live environment + deep tool-arg introspection
//	  3. if blockers → LLM-repair the EXACT problems, re-run; if no progress, stop
//	  4. once clean → VERIFY by actually running it (pluggable Verifier)
//	  5. if the run fails → repair against the REAL runtime error, re-run
//	  6. stop when a real run passes, or the budget/▢no-progress is hit
//
// The loop is pure orchestration over injected seams (an LLM and a Verifier), so
// it is fully unit-testable with fakes. The gateway supplies a real-execution
// Verifier so "verified" means the agent genuinely ran end-to-end against real
// tools — not that a mock said so.
package studio

import (
	"context"
	"fmt"
	"strings"
)

// VerifyOutcome is the result of executing a draft once. OK reports success;
// when false, Error is the runtime failure to repair against. Trace is a short
// human-readable record of what happened, surfaced in the transcript. Real
// reports whether the run actually invoked tools (vs a mocked dry-run).
type VerifyOutcome struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	Trace []string `json:"trace,omitempty"`
	Real  bool     `json:"real"`
}

// Verifier executes a draft for one test case and reports the outcome. Two
// implementations exist: a mocked dry-run (DryRunVerifier) for structural
// confidence with zero side effects, and a real-execution verifier supplied by
// the gateway that runs the agent against real tools. The full TestCase is
// passed so the verifier can evaluate the case's assertions.
type Verifier interface {
	Verify(ctx context.Context, draft Draft, tc TestCase) VerifyOutcome
}

// TestCase is one self-test exercised during verification: an input plus the
// assertions that must hold. Assertions are evaluated by the verifier (the
// dry-run verifier uses EvaluateAssertions; a real verifier may check the final
// output). An empty Assertions slice means "just don't error".
type TestCase struct {
	Input      string      `json:"input"`
	Assertions []Assertion `json:"assertions,omitempty"`
}

// BuildOptions configures one autonomous build.
type BuildOptions struct {
	// MaxAttempts caps the loop. Zero defaults to 6.
	MaxAttempts int
	// In is the live-environment state preflight validates against.
	In PreflightInput
	// Verifier, when set, runs the draft after it passes preflight. When nil the
	// loop is validation-only (it stops once preflight is clean).
	Verifier Verifier
	// Tests are the self-tests to exercise during verification. When empty a
	// single no-input run is used.
	Tests []TestCase
	// OnEvent, when set, is called as the loop progresses so a caller can stream
	// live status to the user (each attempt starting, what it's repairing, when
	// it's running, the outcome). Nil-safe.
	OnEvent func(BuildEvent)
	// Trace, when set, durably records every phase of the loop (draft snapshots,
	// preflight, each repair, each verify, the final result) with timings and
	// structured detail, so a build is fully debuggable after the fact. Nil-safe:
	// all BuildTrace methods are no-ops on a nil trace.
	Trace *BuildTrace
}

// BuildEvent is one live progress update emitted during BuildUntilWorks. Kind is
// "attempt" | "repair" | "verify" | "result". Message is a short, plain-language
// line ready to show the user.
type BuildEvent struct {
	Kind    string `json:"kind"`
	Attempt int    `json:"attempt"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message"`
}

// BuildAttempt records one pass of the loop for the transcript.
type BuildAttempt struct {
	N        int      `json:"n"`
	Phase    string   `json:"phase"`    // "repair" | "verify"
	Problems []string `json:"problems"` // what was wrong at the start of this attempt
	Action   string   `json:"action"`   // what the loop did about it
	Changed  bool     `json:"changed"`  // whether the draft was modified
	OK       bool     `json:"ok"`       // whether this attempt left the draft passing
}

// BuildReport is the full result of an autonomous build: the final draft, a
// verdict, and the complete attempt transcript so the user sees exactly what was
// wrong and how each problem was fixed.
type BuildReport struct {
	Workflow Draft          `json:"workflow"`
	OK       bool           `json:"ok"`       // preflight clean (and verified, if a verifier ran)
	Verified bool           `json:"verified"` // a real/dry run actually passed
	Attempts []BuildAttempt `json:"attempts"`
	Summary  string         `json:"summary"`
	Residual []string       `json:"residual,omitempty"` // problems still unresolved at the end
	// Diagnosis, when set, is a plain-language explanation of WHY the build could
	// not be fixed automatically — and what to do about it. It is set when the
	// loop detects it is repairing the same failure over and over (cosmetic edits
	// that don't converge), most importantly when a fixed flow is fighting a
	// reasoning task and the answer is to switch to an agent (ReAct) instead.
	Diagnosis string `json:"diagnosis,omitempty"`
	// SuggestMode, when non-empty ("react"|"plan_execute"), tells the GUI the loop
	// believes this task should be authored as an agent in that mode, so it can
	// offer a one-click switch. Embodies "Studio figures it out, not the user."
	SuggestMode string `json:"suggest_mode,omitempty"`
}

// BuildUntilWorks drives the autonomous build-verify-repair loop over an
// already-generated draft. It returns a BuildReport with the best draft it could
// reach and a transparent transcript of every attempt. It never panics on a bad
// model or verifier — failures degrade to a report explaining what's still wrong.
func BuildUntilWorks(ctx context.Context, llm LLM, draft Draft, cat Catalog, opts BuildOptions) BuildReport {
	max := opts.MaxAttempts
	if max <= 0 {
		max = 6
	}
	rep := BuildReport{Workflow: draft}
	tr := opts.Trace // nil-safe throughout
	emit := func(ev BuildEvent) {
		tr.Event(ev) // durably record every user-facing progress line
		if opts.OnEvent != nil {
			opts.OnEvent(ev)
		}
	}
	tr.Logd("phase", "loop", 0, "build-verify-repair loop starting",
		map[string]any{"max_attempts": max, "verify": opts.Verifier != nil, "tests": len(opts.Tests)})

	verified := false
	preflightClean := false
	// Non-convergence tracking for the VERIFY phase. We distinguish two cases so a
	// legitimately-progressing repair isn't cut off after a single pass:
	//   • seenExact — the IDENTICAL runtime error text recurred ⇒ the last repair
	//     changed nothing that mattered ⇒ stop now (truly stuck).
	//   • sigCount — the same coarse error CLASS (node|kind) recurred but with
	//     different specifics (e.g. fixed arg A, now arg B of the same tool) ⇒ that
	//     is progress; allow up to maxRepairsPerClass passes before giving up.
	seenExact := map[string]bool{}
	sigCount := map[string]int{}
	const maxRepairsPerClass = 2
	// seenProblemSets does the same for the PREFLIGHT/repair phase: if the exact
	// same blocker set comes back after a repair claimed to change the draft, the
	// loop isn't converging — stop rather than re-repairing the same problems.
	seenProblemSets := map[string]bool{}

	for n := 1; n <= max; n++ {
		emit(BuildEvent{Kind: "attempt", Attempt: n, Message: fmt.Sprintf("Attempt %d — checking the draft against your setup…", n)})
		tr.Snapshot("attempt-start", n, rep.Workflow)

		// (1) Deterministic repair first — free, and it shrinks the problem set
		// the model has to reason about.
		RepairWiring(&rep.Workflow, cat)

		// (2) Validate against the live environment + deep tool introspection.
		donePf := tr.Step("preflight", "repair", n, "preflight against the live environment")
		pf := Preflight(rep.Workflow, opts.In)
		problems := buildProblemSet(pf, rep.Workflow, cat)
		donePf(nil, map[string]any{
			"blockers": len(pf.Blockers), "warnings": len(pf.Warnings),
			"problems": problems,
		})

		if len(problems) > 0 {
			emit(BuildEvent{Kind: "repair", Attempt: n, Phase: "repair", Message: fmt.Sprintf("Found %s — repairing…", plural(len(problems), "problem"))})
			att := BuildAttempt{N: n, Phase: "repair", Problems: problems}

			// Repair non-convergence: if this EXACT blocker set already came back
			// after a prior repair, editing the flow isn't resolving it. Stop —
			// and if the residual is reasoning-fit (a fixed flow carrying an agent
			// task), recommend ReAct instead of looping to the attempt budget.
			problemSig := strings.Join(dedupeStrings(problems), "\n")
			if seenProblemSets[problemSig] {
				att.Action = "same problems recurred after repair — stopping (not converging)"
				att.Changed = false
				rep.Attempts = append(rep.Attempts, att)
				rep.Residual = problems
				if problemsAreReasoningFit(problems) {
					rep.Diagnosis, rep.SuggestMode = reasoningFitDiagnosis()
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: rep.Diagnosis})
				} else {
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: "Couldn't fix it automatically — see remaining problems."})
				}
				tr.Logd("repair", "repair", n, att.Action, map[string]any{"problems": problems, "suggest_mode": rep.SuggestMode})
				break
			}
			seenProblemSets[problemSig] = true

			// (3) Repair the exact problems. Try the general full-draft LLM repair
			// first; fall back to a deterministic focused repair if the model
			// can't help. If neither makes progress, stop — looping is pointless.
			repaired, changed := RepairWithProblems(ctx, llm, rep.Workflow, problems, cat)
			if changed {
				RepairWiring(&repaired, cat)
				rep.Workflow = repaired
				att.Action = "LLM repair of " + plural(len(problems), "problem")
				att.Changed = true
			} else if focusedRepair(ctx, llm, &rep.Workflow) {
				att.Action = "focused repair of broken steps"
				att.Changed = true
			} else {
				att.Action = "no automated fix available — stopping"
				att.Changed = false
				rep.Attempts = append(rep.Attempts, att)
				rep.Residual = problems
				// If the unfixable blockers are reasoning-fit, this flow can't be
				// repaired in workflow mode — point the user at ReAct.
				if problemsAreReasoningFit(problems) {
					rep.Diagnosis, rep.SuggestMode = reasoningFitDiagnosis()
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: rep.Diagnosis})
				} else {
					emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: "Couldn't fix it automatically — see remaining problems."})
				}
				tr.Logd("repair", "repair", n, att.Action, map[string]any{"problems": problems, "changed": false, "suggest_mode": rep.SuggestMode})
				break
			}
			emit(BuildEvent{Kind: "repair", Attempt: n, Phase: "repair", Message: "✓ " + att.Action})
			tr.Logd("repair", "repair", n, att.Action, map[string]any{"problems": problems, "changed": att.Changed})
			rep.Attempts = append(rep.Attempts, att)
			continue
		}

		// Preflight is clean.
		preflightClean = true

		// (4) No verifier → validation-only build is done.
		if opts.Verifier == nil {
			rep.Attempts = append(rep.Attempts, BuildAttempt{
				N: n, Phase: "verify", Action: "validation passed (no execution requested)", OK: true,
			})
			break
		}

		// (4) Verify by actually running it.
		emit(BuildEvent{Kind: "verify", Attempt: n, Phase: "verify", Message: "Validation passed — running it to verify…"})
		doneV := tr.Step("verify", "verify", n, "running the agent to verify it works")
		out := verifyAll(ctx, opts.Verifier, rep.Workflow, opts.Tests)
		doneV(errOrNil(out.Error), map[string]any{
			"ok": out.OK, "real": out.Real, "error": out.Error, "run_trace": out.Trace,
		})
		att := BuildAttempt{N: n, Phase: "verify", OK: out.OK}
		if out.OK {
			att.Action = runWord(out.Real) + " succeeded"
			att.OK = true
			emit(BuildEvent{Kind: "result", Attempt: n, Phase: "verify", Message: "✓ " + att.Action})
			rep.Attempts = append(rep.Attempts, att)
			verified = true
			break
		}

		// (5) Runtime failure. Stop only when the loop is genuinely not converging:
		// the identical error came back (last repair was cosmetic), or the same
		// error class has now failed more than maxRepairsPerClass times. A class
		// recurring with NEW specifics is progress and gets another pass.
		sig := errSignature(out.Error)
		if seenExact[out.Error] || sigCount[sig] >= maxRepairsPerClass {
			att.Action = "same failure recurred after repair — stopping (not converging)"
			att.Problems = []string{out.Error}
			rep.Residual = []string{out.Error}
			rep.Diagnosis, rep.SuggestMode = nonConvergenceDiagnosis(out.Error)
			emit(BuildEvent{Kind: "result", Attempt: n, Phase: "verify", Message: rep.Diagnosis})
			tr.Logd("repair", "verify", n, att.Action, map[string]any{"runtime_error": out.Error, "signature": sig, "suggest_mode": rep.SuggestMode})
			rep.Attempts = append(rep.Attempts, att)
			break
		}
		seenExact[out.Error] = true
		sigCount[sig]++

		emit(BuildEvent{Kind: "verify", Attempt: n, Phase: "verify", Message: "Run failed: " + out.Error + " — repairing…"})
		att.Problems = []string{out.Error}
		repaired, changed := RepairWithProblems(ctx, llm, rep.Workflow,
			[]string{"At RUN TIME the agent failed with this error — change it so this cannot happen again: " + out.Error}, cat)
		if changed {
			RepairWiring(&repaired, cat)
			rep.Workflow = repaired
			att.Action = "repaired against runtime error"
			att.Changed = true
			tr.Logd("repair", "verify", n, att.Action, map[string]any{"runtime_error": out.Error, "changed": true})
			rep.Attempts = append(rep.Attempts, att)
			continue
		}
		att.Action = "runtime error persists — no automated fix available"
		tr.Logd("repair", "verify", n, att.Action, map[string]any{"runtime_error": out.Error, "changed": false})
		rep.Attempts = append(rep.Attempts, att)
		rep.Residual = []string{out.Error}
		// Even on the first try, a handoff/parse error is a flow-vs-reasoning
		// mismatch the loop can't fix by editing the flow — recommend agent mode.
		if isHandoffReasoningError(out.Error) {
			rep.Diagnosis, rep.SuggestMode = nonConvergenceDiagnosis(out.Error)
			emit(BuildEvent{Kind: "result", Attempt: n, Phase: "verify", Message: rep.Diagnosis})
		}
		break
	}

	rep.Verified = verified
	rep.OK = preflightClean && (opts.Verifier == nil || verified)
	rep.Summary = buildSummary(rep)
	tr.Snapshot("final", 0, rep.Workflow)
	tr.Logd("result", "done", 0, rep.Summary, map[string]any{
		"ok": rep.OK, "verified": rep.Verified,
		"attempts": len(rep.Attempts), "residual": rep.Residual,
	})
	return rep
}

// errSignature reduces a runtime error to a stable class so the loop can tell
// when it is repairing the same failure over and over. It keeps the node id and
// the structural SHAPE of the error but drops the specific field name / offsets
// the model keeps permuting (.parsed.skill → .parsed.skill_name → …), so two
// "different" errors that are really the same root cause collapse to one key.
func errSignature(msg string) string {
	s := strings.ToLower(msg)
	node := ""
	if i := strings.Index(s, `node "`); i >= 0 {
		if j := strings.Index(s[i+6:], `"`); j >= 0 {
			node = s[i+6 : i+6+j]
		}
	}
	kind := "runtime"
	switch {
	case strings.Contains(s, "can't evaluate field") && strings.Contains(s, "interface {}"):
		kind = "template-field-on-untyped"
	case strings.Contains(s, "execute template"), strings.Contains(s, "render input"):
		kind = "template-render"
	case strings.Contains(s, "unmarshal"), strings.Contains(s, "invalid character") && strings.Contains(s, "json"):
		kind = "json-parse"
	}
	return node + "|" + kind
}

// isHandoffReasoningError reports whether a runtime error is the signature of a
// FIXED FLOW trying to mechanically read structure out of an upstream step's
// FREE-FORM output (an LLM/agent reply, typed interface{}): template-field /
// template-render / json-parse failures. These don't get fixed by renaming the
// field — the upstream has no guaranteed shape. The task is a reasoning task.
func isHandoffReasoningError(msg string) bool {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "can't evaluate field") && strings.Contains(msg, "interface {}"):
		return true
	case strings.Contains(msg, "execute template"), strings.Contains(msg, "render input"):
		return true
	case strings.Contains(low, "unmarshal"):
		return true
	// Preflight phrasing: a fixed step references a variable (the user's message,
	// conversation context, a channel, …) that no upstream step can produce. In a
	// fixed graph there is nowhere for that value to come from — it's inherently a
	// reasoning-agent input. This is the dominant signature of a flow miscast from
	// an agent task (the finance-assistant case).
	case strings.Contains(low, "no earlier step produces"):
		return true
	}
	return false
}

// problemsAreReasoningFit reports whether the residual build problems are, in the
// main, the signature of a fixed flow carrying a reasoning task (steps wired to
// values nothing produces, template/parse handoff failures). When they are, no
// amount of flow editing will fix it — the right move is an agent (ReAct).
func problemsAreReasoningFit(problems []string) bool {
	if len(problems) == 0 {
		return false
	}
	hits := 0
	for _, p := range problems {
		if isHandoffReasoningError(p) {
			hits++
		}
	}
	return hits*2 >= len(problems) // a majority are reasoning-fit blockers
}

// reasoningFitDiagnosis is the verdict shown when the residual blockers say the
// task belongs in an agent, not a fixed flow. Returns the message + suggest mode.
func reasoningFitDiagnosis() (diagnosis, suggestMode string) {
	return "These steps reference values — like the user's message, the conversation context, or the channel — that no fixed step can produce. The workflow is being asked to route and reason over free-form input, which is an agent's job, not a fixed graph. Switch to the ReAct reasoning loop and the agent will pick the right skill/tool per request.", "react"
}

// nonConvergenceDiagnosis explains why the loop gave up and what to do about it.
// When the recurring failure is a handoff/parse error, the verdict is decisive:
// this is a reasoning task wrongly cast as a fixed pipeline — author it as an
// agent (ReAct) and let the agent pick the skill/tool per query. Returns the
// human-readable diagnosis plus a suggested mode for the GUI's one-click switch.
func nonConvergenceDiagnosis(err string) (diagnosis, suggestMode string) {
	if isHandoffReasoningError(err) {
		return "This step keeps failing because a fixed workflow is trying to read fields out of an upstream step's free-form output (an LLM or agent reply), which has no guaranteed shape — each repair just renames the field and it breaks again. This is a reasoning task, not a fixed pipeline: switch it to an agent with the ReAct mode toggle and let the agent choose the skill/tool per request.", "react"
	}
	return "The same failure recurred after repair, so automatic fixing stopped to avoid looping. Review the run error above and adjust the step, or try authoring this as an agent (ReAct) if the flow is fighting free-form data.", ""
}

// errOrNil adapts a string error field to an error for the trace Step closure.
func errOrNil(msg string) error {
	if msg == "" {
		return nil
	}
	return fmt.Errorf("%s", msg)
}

// buildProblemSet turns a preflight report into the concrete problem lines the
// repair loop acts on: the BLOCKERS only (a required argument left empty, a
// dangling reference, a missing entry, …) — the things that are definitely wrong.
//
// Deep-introspection argument WARNINGS ("argument X is not accepted by tool Y")
// are deliberately NOT included. They compare a node's args against the MCP
// server's *published* schema, which is frequently incomplete — a tool routinely
// accepts arguments it never advertises (e.g. NotebookLM's `max_wait` / `task_id`,
// confirmed working in real runs). Feeding those false positives to the loop sends
// it chasing phantom errors it can't resolve, burning its whole attempt budget.
// Real verification is the authority: if an argument is genuinely rejected, the
// tool errors when the verify phase runs it, and the loop repairs against that
// real error. So warnings still inform the user (Studio validation surfaces them)
// but never drive the autonomous build. (Pure-config warnings — missing channel
// token, unattended tradeoff — are likewise excluded: they need the user.)
func buildProblemSet(pf PreflightResult, draft Draft, cat Catalog) []string {
	var out []string
	for _, b := range pf.Blockers {
		out = append(out, issueLine(b))
	}
	return dedupeStrings(out)
}

// issueLine renders a preflight issue as a single repair instruction.
func issueLine(i PreflightIssue) string {
	out := i.Message
	if i.NodeID != "" {
		out = "step \"" + i.NodeID + "\": " + out
	}
	if i.Fix != "" {
		out += " (" + i.Fix + ")"
	}
	return out
}

// verifyAll runs every test case through the verifier and returns the first
// failure (so the loop repairs one concrete error at a time), or an aggregate
// success. With no tests, a single empty-input run is performed.
func verifyAll(ctx context.Context, v Verifier, draft Draft, tests []TestCase) VerifyOutcome {
	if len(tests) == 0 {
		tests = []TestCase{{}}
	}
	var anyReal bool
	var trace []string
	for i, tc := range tests {
		out := v.Verify(ctx, draft, tc)
		anyReal = anyReal || out.Real
		trace = append(trace, out.Trace...)
		if !out.OK {
			out.Trace = trace
			if out.Error == "" {
				out.Error = fmt.Sprintf("test %d failed with no error detail", i+1)
			}
			return out
		}
	}
	return VerifyOutcome{OK: true, Real: anyReal, Trace: trace}
}

func runWord(real bool) string {
	if real {
		return "real run"
	}
	return "dry run"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// buildSummary writes a one-line plain-language verdict for the report header.
func buildSummary(rep BuildReport) string {
	switch {
	case rep.OK && rep.Verified:
		return fmt.Sprintf("Built and verified by running it — %s.", attemptsPhrase(rep))
	case rep.OK:
		return fmt.Sprintf("Validated clean against your setup — %s. Run it to verify end-to-end.", attemptsPhrase(rep))
	case rep.Diagnosis != "":
		return rep.Diagnosis
	case len(rep.Residual) > 0:
		return "Could not fully fix it automatically. Remaining: " + strings.Join(rep.Residual, "; ")
	default:
		return "Stopped without a clean build; see the attempt log."
	}
}

func attemptsPhrase(rep BuildReport) string {
	repairs := 0
	for _, a := range rep.Attempts {
		if a.Phase == "repair" && a.Changed {
			repairs++
		}
	}
	if repairs == 0 {
		return "no repairs were needed"
	}
	return "after " + plural(repairs, "automatic fix")
}

// dedupeStrings removes duplicate lines, preserving order.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
