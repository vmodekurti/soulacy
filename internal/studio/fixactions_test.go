package studio

// The vocabulary itself, and the guarantee that nothing emits outside it.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestFixActions_EveryEntryIsUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range FixActions() {
		if a.ID == "" {
			t.Fatal("an action with no id can never be matched")
		}
		if seen[a.ID] {
			t.Fatalf("duplicate action id %q — the second entry's label is unreachable", a.ID)
		}
		seen[a.ID] = true
		if strings.TrimSpace(a.Label) == "" {
			t.Errorf("action %q has no label, so it renders no button", a.ID)
		}
		switch a.Kind {
		case FixKindNavigate, FixKindApply, FixKindFocus:
		default:
			t.Errorf("action %q has unknown kind %q", a.ID, a.Kind)
		}
	}
}

func TestFixActionLabel_UnknownIDReturnsEmpty(t *testing.T) {
	// A plausible-looking default would hide the bug behind a button that does
	// nothing, which is the failure this whole file exists to prevent.
	if got := FixActionLabel("not_a_real_action"); got != "" {
		t.Fatalf("unknown id should have no label, got %q", got)
	}
	if IsFixAction("not_a_real_action") {
		t.Fatal("unknown id should not validate")
	}
}

func TestResolveFixLabel_PrefersTheFindingsOwnWording(t *testing.T) {
	if got := resolveFixLabel(FixOpenDelivery, "Take me to the binding"); got != "Take me to the binding" {
		t.Fatalf("override should win, got %q", got)
	}
	if got := resolveFixLabel(FixOpenDelivery, "  "); got != "Open Delivery" {
		t.Fatalf("blank override should fall back to the vocabulary, got %q", got)
	}
}

// Nothing anywhere in the studio package may emit an action id the vocabulary
// does not declare — the client has no handler for it, so it renders a dead
// button or (since finishItem drops unknowns) silently loses the fix.
func TestNoActionIsEmittedOutsideTheVocabulary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`Action:\s*"([a-z_]+)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if !IsFixAction(m[1]) {
				t.Errorf("%s emits action %q, which is not in the vocabulary — add it to fixactions.go (and to the client)", f, m[1])
			}
		}
	}
}

// A node-scoped finding should point AT the node, not at the editor the user is
// already looking at.
func TestFinishItem_NodeScopedFindingsRevealTheNode(t *testing.T) {
	got := finishItem(ReadinessItem{Action: FixOpenStudio, NodeID: "parse_results"}, "")
	if got.Action != FixRevealNode {
		t.Fatalf("expected reveal_node for a node-scoped finding, got %q", got.Action)
	}
	if got.ActionLabel != "Show the step" {
		t.Fatalf("label should follow the resolved action, got %q", got.ActionLabel)
	}

	// Without a node there is nothing to reveal, so it stays as it was.
	if got := finishItem(ReadinessItem{Action: FixOpenStudio}, ""); got.Action != FixOpenStudio {
		t.Fatalf("expected open_studio to survive when there is no node, got %q", got.Action)
	}
}

func TestFinishItem_DropsAnUnhandleableAction(t *testing.T) {
	got := finishItem(ReadinessItem{Action: "teleport", Fix: "do the thing"}, "")
	if got.Action != "" {
		t.Fatalf("an id the client cannot handle must not reach it, got %q", got.Action)
	}
	if got.ActionLabel != "" {
		t.Fatalf("no action means no button, got label %q", got.ActionLabel)
	}
	if got.Fix == "" {
		t.Fatal("dropping the action must not drop the written fix — it is all the user has left")
	}
}

// The readiness view is the one place every finding type meets, so it is also
// the one place a finding's own fix can be silently replaced by a generic one.
// It was: security findings had their action overwritten by a category-derived
// "go to this screen", so the panel offered navigation for a fix Studio could
// have applied outright.
func TestReadinessItems_KeepAFindingsOwnAction(t *testing.T) {
	items := securityItems(SecurityReview{
		Warnings: []SecurityFinding{{
			Severity: "warn", Category: "channel",
			Message:     "shared channel exposure",
			Fix:         "…",
			Action:      FixInternalChannelsOnly,
			ActionLabel: "Use internal channels only",
		}},
	})
	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}
	if items[0].Action != FixInternalChannelsOnly {
		t.Fatalf("the finding's own fix was replaced by a category default: %q", items[0].Action)
	}
	if items[0].ActionLabel != "Use internal channels only" {
		t.Fatalf("the finding's own button text was lost: %q", items[0].ActionLabel)
	}
}

// A finding with no opinion still gets a sensible destination from its category.
func TestReadinessItems_FallBackToTheCategoryMapping(t *testing.T) {
	items := securityItems(SecurityReview{
		Warnings: []SecurityFinding{{Severity: "warn", Category: "channel", Message: "…"}},
	})
	if items[0].Action != FixOpenDelivery {
		t.Fatalf("expected the category fallback, got %q", items[0].Action)
	}
	if items[0].ActionLabel == "" {
		t.Fatal("a fallback action still needs button text")
	}
}

// Contract checks get the same treatment: their own action wins, the id-derived
// mapping is only the fallback.
func TestReadinessItems_KeepAContractChecksOwnAction(t *testing.T) {
	items := contractItems(ContractResult{Checks: []ContractCheck{
		{ID: "security.intent_gate", Status: "warn", Message: "…", Action: FixIntentGateDeny},
		{ID: "runtime.provider", Status: "block", Message: "…"},
	}})
	if items[0].Action != FixIntentGateDeny {
		t.Fatalf("the check's own fix was replaced: %q", items[0].Action)
	}
	if items[1].Action != FixOpenProviders {
		t.Fatalf("expected the id-derived fallback for runtime.provider, got %q", items[1].Action)
	}
}
