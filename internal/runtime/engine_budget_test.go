package runtime

import "testing"

// TestBudgetExceeded covers the per-run budget gate logic (S3.1).
func TestBudgetExceeded(t *testing.T) {
	cases := []struct {
		name                                         string
		tokenLimit, usedTokens, callLimit, usedCalls int
		wantHalt                                     bool
	}{
		{"no limits", 0, 1_000_000, 0, 1000, false},
		{"under token limit", 100, 99, 0, 0, false},
		{"at token limit", 100, 100, 0, 0, true},
		{"over token limit", 100, 250, 0, 0, true},
		{"under call limit", 0, 0, 5, 4, false},
		{"at call limit", 0, 0, 5, 5, true},
		{"token ok call over", 1000, 10, 3, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := budgetExceeded(tc.tokenLimit, tc.usedTokens, tc.callLimit, tc.usedCalls)
			if (got != "") != tc.wantHalt {
				t.Fatalf("budgetExceeded(%d,%d,%d,%d) = %q; wantHalt=%v",
					tc.tokenLimit, tc.usedTokens, tc.callLimit, tc.usedCalls, got, tc.wantHalt)
			}
		})
	}
}

// TestTurnsCeiling verifies the server-side max_turns ceiling resolution (S3.2).
func TestTurnsCeiling(t *testing.T) {
	e := &Engine{}
	if got := e.turnsCeiling(); got != defaultMaxTurnsCeiling {
		t.Fatalf("unset ceiling should default to %d, got %d", defaultMaxTurnsCeiling, got)
	}
	e.SetMaxTurnsCeiling(0) // 0 → default
	if got := e.turnsCeiling(); got != defaultMaxTurnsCeiling {
		t.Fatalf("SetMaxTurnsCeiling(0) should default to %d, got %d", defaultMaxTurnsCeiling, got)
	}
	e.SetMaxTurnsCeiling(12)
	if got := e.turnsCeiling(); got != 12 {
		t.Fatalf("ceiling = %d, want 12", got)
	}
}
