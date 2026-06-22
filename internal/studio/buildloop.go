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
	emit := func(ev BuildEvent) {
		if opts.OnEvent != nil {
			opts.OnEvent(ev)
		}
	}

	verified := false
	preflightClean := false

	for n := 1; n <= max; n++ {
		emit(BuildEvent{Kind: "attempt", Attempt: n, Message: fmt.Sprintf("Attempt %d — checking the draft against your setup…", n)})

		// (1) Deterministic repair first — free, and it shrinks the problem set
		// the model has to reason about.
		RepairWiring(&rep.Workflow, cat)

		// (2) Validate against the live environment + deep tool introspection.
		pf := Preflight(rep.Workflow, opts.In)
		problems := buildProblemSet(pf, rep.Workflow, cat)

		if len(problems) > 0 {
			emit(BuildEvent{Kind: "repair", Attempt: n, Phase: "repair", Message: fmt.Sprintf("Found %s — repairing…", plural(len(problems), "problem"))})
			// (3) Repair the exact problems. Try the general full-draft LLM repair
			// first; fall back to a deterministic focused repair if the model
			// can't help. If neither makes progress, stop — looping is pointless.
			att := BuildAttempt{N: n, Phase: "repair", Problems: problems}
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
				emit(BuildEvent{Kind: "result", Attempt: n, Phase: "repair", Message: "Couldn't fix it automatically — see remaining problems."})
				rep.Attempts = append(rep.Attempts, att)
				rep.Residual = problems
				break
			}
			emit(BuildEvent{Kind: "repair", Attempt: n, Phase: "repair", Message: "✓ " + att.Action})
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
		out := verifyAll(ctx, opts.Verifier, rep.Workflow, opts.Tests)
		att := BuildAttempt{N: n, Phase: "verify", OK: out.OK}
		if out.OK {
			att.Action = runWord(out.Real) + " succeeded"
			att.OK = true
			emit(BuildEvent{Kind: "result", Attempt: n, Phase: "verify", Message: "✓ " + att.Action})
			rep.Attempts = append(rep.Attempts, att)
			verified = true
			break
		}

		// (5) Runtime failure → repair against the REAL error and re-run.
		emit(BuildEvent{Kind: "verify", Attempt: n, Phase: "verify", Message: "Run failed: " + out.Error + " — repairing…"})
		att.Problems = []string{out.Error}
		repaired, changed := RepairWithProblems(ctx, llm, rep.Workflow,
			[]string{"At RUN TIME the agent failed with this error — change it so this cannot happen again: " + out.Error}, cat)
		if changed {
			RepairWiring(&repaired, cat)
			rep.Workflow = repaired
			att.Action = "repaired against runtime error"
			att.Changed = true
			rep.Attempts = append(rep.Attempts, att)
			continue
		}
		att.Action = "runtime error persists — no automated fix available"
		rep.Attempts = append(rep.Attempts, att)
		rep.Residual = []string{out.Error}
		break
	}

	rep.Verified = verified
	rep.OK = preflightClean && (opts.Verifier == nil || verified)
	rep.Summary = buildSummary(rep)
	return rep
}

// buildProblemSet turns a preflight report into the concrete problem lines the
// repair loop acts on: all blockers, plus the deep-introspection argument
// warnings (unknown/mistyped tool arguments) which are silent runtime killers.
// Pure-config warnings (missing channel token, unattended tradeoff) are NOT
// included — those need the user, not a code change.
func buildProblemSet(pf PreflightResult, draft Draft, cat Catalog) []string {
	var out []string
	for _, b := range pf.Blockers {
		out = append(out, issueLine(b))
	}
	for _, w := range pf.Warnings {
		// Only the actionable, code-fixable argument warnings.
		if w.Kind == "dependency" {
			out = append(out, issueLine(w))
		}
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
