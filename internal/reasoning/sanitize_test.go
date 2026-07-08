package reasoning

import (
	"strings"
	"testing"
)

func TestSanitize_PassesThroughRealAnswer(t *testing.T) {
	ans := "Here is your playlist: 20 Carnatic jazz-fusion tracks added."
	if got := SanitizeFinalOutput(ans, nil); got != ans {
		t.Fatalf("real answer was altered: %q", got)
	}
}

func TestSanitize_ExtractsFinalAnswerFromControlJSON(t *testing.T) {
	in := `{"thought":"done now","is_done":true,"final_answer":"Playlist created with 20 tracks."}`
	got := SanitizeFinalOutput(in, nil)
	if got != "Playlist created with 20 tracks." {
		t.Fatalf("expected extracted final_answer, got %q", got)
	}
}

func TestSanitize_ExtractsOutputField(t *testing.T) {
	in := `{"output":"All done."}`
	// Bare {"output":...} is NOT control JSON (no thought/action/is_done) → passes through unchanged.
	if got := SanitizeFinalOutput(in, nil); got != in {
		t.Fatalf("bare output JSON should pass through, got %q", got)
	}
	// But with control fields present, output is extracted.
	in2 := `{"thought":"finishing","action":null,"output":"All done."}`
	if got := SanitizeFinalOutput(in2, nil); got != "All done." {
		t.Fatalf("expected extracted output, got %q", got)
	}
}

func TestSanitize_LeakedStepWithNoAnswer_FallsBack(t *testing.T) {
	in := `{"thought":"Now I'll search spotify","is_done":false,"action":{"tool":"python_eval","input":{"code":"import requests"}}}`
	got := SanitizeFinalOutput(in, nil)
	if strings.Contains(got, "python_eval") || strings.Contains(got, "\"thought\"") {
		t.Fatalf("control JSON leaked into output: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "reasoning") {
		t.Fatalf("expected graceful fallback message, got %q", got)
	}
}

func TestSanitize_LeakedStep_UsesReadableObservation(t *testing.T) {
	steps := []Step{
		{Thought: "search", Obs: Observation{Content: "Found 20 tracks; playlist URL: https://open.spotify.com/x"}},
	}
	in := `{"thought":"continuing","is_done":false,"action":{"tool":"python_eval"}}`
	got := SanitizeFinalOutput(in, steps)
	if !strings.Contains(got, "Found 20 tracks") {
		t.Fatalf("expected fallback to readable observation, got %q", got)
	}
}

func TestSanitize_FencedControlJSON(t *testing.T) {
	in := "```json\n{\"thought\":\"x\",\"is_done\":false,\"action\":{\"tool\":\"t\"}}\n```"
	got := SanitizeFinalOutput(in, nil)
	if strings.Contains(got, "thought") {
		t.Fatalf("fenced control JSON leaked: %q", got)
	}
}

func TestSanitize_EmptyFallsBack(t *testing.T) {
	if got := SanitizeFinalOutput("   ", nil); !strings.Contains(strings.ToLower(got), "reasoning") {
		t.Fatalf("empty output should yield graceful message, got %q", got)
	}
}

func TestSanitize_LegitJSONAnswerPreserved(t *testing.T) {
	// An agent legitimately returning JSON data (no control fields) is untouched.
	in := `{"name":"Alice","score":42}`
	if got := SanitizeFinalOutput(in, nil); got != in {
		t.Fatalf("legit JSON answer was altered: %q", got)
	}
}
