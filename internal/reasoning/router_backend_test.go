package reasoning

import (
	"context"
	"strings"
	"testing"
)

// fakeCompleter returns a canned reply and records the last call, standing in
// for the gateway's router so RouterBackend can be tested without a real model.
type fakeCompleter struct {
	reply     string
	err       error
	lastModel string
	lastSys   string
	lastUser  string
}

func (f *fakeCompleter) Complete(ctx context.Context, model, system, user string, maxTokens int) (string, error) {
	f.lastModel, f.lastSys, f.lastUser = model, system, user
	return f.reply, f.err
}

func TestRouterBackend_ThinkParsesJSON(t *testing.T) {
	fc := &fakeCompleter{reply: `{"thought":"search first","is_done":false,"action":{"tool":"web_search","input":{"q":"flights"}}}`}
	b := NewRouterBackend(fc, "gemini-2.5-pro")

	resp, err := b.Think(context.Background(), ThinkRequest{TaskInput: "find flights", ToolNames: []string{"web_search"}})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if resp.IsDone {
		t.Fatal("expected not done")
	}
	if resp.Action.Tool != "web_search" {
		t.Fatalf("tool = %q, want web_search", resp.Action.Tool)
	}
	if fc.lastModel != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want gemini-2.5-pro", fc.lastModel)
	}
	// The tool list and JSON instruction must reach the model.
	if !strings.Contains(fc.lastSys, "web_search") || !strings.Contains(fc.lastSys, "JSON") {
		t.Fatalf("system prompt missing tools/JSON instruction: %q", fc.lastSys)
	}
}

func TestRouterBackend_ThinkHandlesMarkdownFences(t *testing.T) {
	fc := &fakeCompleter{reply: "```json\n{\"thought\":\"done\",\"is_done\":true,\"final_answer\":\"hello\"}\n```"}
	b := NewRouterBackend(fc, "qwen3:32b")
	resp, err := b.Think(context.Background(), ThinkRequest{TaskInput: "x"})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if !resp.IsDone || resp.FinalAnswer != "hello" {
		t.Fatalf("fenced JSON not parsed: %+v", resp)
	}
}

func TestRouterBackend_ReflectParsesJSON(t *testing.T) {
	fc := &fakeCompleter{reply: `{"output":"Top 3 flights: ...","updated_rules":""}`}
	b := NewRouterBackend(fc, "glm-5.2")
	resp, err := b.Reflect(context.Background(), ReflectRequest{TaskInput: "find flights"})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if resp.Output != "Top 3 flights: ..." {
		t.Fatalf("output = %q", resp.Output)
	}
}

func TestRouterBackend_ReflectFallsBackToRawText(t *testing.T) {
	// A model that ignores the JSON instruction and replies in prose must still
	// yield a non-empty answer rather than an empty output.
	fc := &fakeCompleter{reply: "Here are your best options: Delta $210, United $245."}
	b := NewRouterBackend(fc, "glm-5.2")
	resp, err := b.Reflect(context.Background(), ReflectRequest{TaskInput: "find flights"})
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if !strings.Contains(resp.Output, "Delta $210") {
		t.Fatalf("prose answer dropped: %q", resp.Output)
	}
}

func TestRouterBackend_PlanParsesJSON(t *testing.T) {
	fc := &fakeCompleter{reply: `{"goal":"book flight","steps":[{"id":"step-1","description":"search","tool":"web_search","depends_on":[]}]}`}
	b := NewRouterBackend(fc, "gpt-4o")
	plan, err := b.Plan(context.Background(), "system", "find flights", 5)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Tool != "web_search" {
		t.Fatalf("plan not parsed: %+v", plan)
	}
}
