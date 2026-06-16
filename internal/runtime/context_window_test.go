package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/llm"
)

func TestModelContextLimit(t *testing.T) {
	cases := map[string]int{
		"claude-3-5-sonnet-20241022": 200000,
		"gpt-4o-mini":                128000,
		"gpt-3.5-turbo":              16385,
		"gemini-2.5-pro":             1000000,
		"qwen2.5:72b":                32768,
		"some-unknown-model":         defaultContextLimit,
	}
	for model, want := range cases {
		if got := modelContextLimit("", model); got != want {
			t.Errorf("modelContextLimit(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestTrimMessagesToFit(t *testing.T) {
	big := strings.Repeat("x", 4000) // ~1000 tokens each
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: big},
		{Role: "assistant", Content: big},
		{Role: "user", Content: big},
		{Role: "assistant", Content: "recent"},
	}
	// Budget that forces dropping some of the old big turns.
	out, dropped := trimMessagesToFit(msgs, nil, 1500)
	if dropped == 0 {
		t.Fatal("expected some messages to be dropped")
	}
	// System message must be preserved at the front.
	if out[0].Role != "system" {
		t.Fatalf("system message must be preserved at front, got role %q", out[0].Role)
	}
	// The most recent message must survive.
	if out[len(out)-1].Content != "recent" {
		t.Fatalf("most recent message should be kept, got %q", out[len(out)-1].Content)
	}
	if estimateTokens(out, nil) > 1500 {
		t.Fatalf("trimmed estimate %d still over budget 1500", estimateTokens(out, nil))
	}
}

func TestTrimDropsOrphanToolResultAtFront(t *testing.T) {
	big := strings.Repeat("y", 8000)
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: big},
		{Role: "tool", Content: "tool result that would otherwise lead"},
		{Role: "user", Content: "latest"},
	}
	out, dropped := trimMessagesToFit(msgs, nil, 500)
	if dropped == 0 {
		t.Fatal("expected trimming")
	}
	// No surviving non-system message at the front may be an orphan tool result.
	if len(out) > 1 && out[1].Role == "tool" {
		t.Fatalf("trimmed history must not start (after system) with a tool result: %+v", out)
	}
}

func TestIsContextExceededErr(t *testing.T) {
	if !isContextExceededErr(errors.New("This model's maximum context length is 8192 tokens")) {
		t.Error("should match OpenAI-style context error")
	}
	if !isContextExceededErr(errors.New("input is too long for requested model")) {
		t.Error("should match 'input is too long'")
	}
	if isContextExceededErr(errors.New("401 unauthorized")) {
		t.Error("auth error must not be classified as context-exceeded")
	}
	if isContextExceededErr(nil) {
		t.Error("nil must not be context-exceeded")
	}
}
