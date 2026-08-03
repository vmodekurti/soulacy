package studio

import (
	"context"
	"encoding/json"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
	"strings"
	"testing"
)

func trvlCatalog() Catalog {
	return Catalog{
		Tools: []string{"web_search", "fetch_url", "channel.send"},
		MCP: []CatalogMCPServer{{
			Server: "trvl",
			Tools: []CatalogMCPTool{
				{Name: "mcp__trvl__travel", Description: "Search flights, hotels and itineraries.", Params: "query*"},
			},
		}},
		Channels: []string{"telegram"},
	}
}

const trvlWorkflowIntent = "Every weekday at 7am search for flight and hotel deals " +
	"using the trvl MCP travel tool and send a digest to telegram"

func graphJSON(tool string) string {
	d := Draft{
		Name:    "Travel Digest",
		Trigger: Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * 1-5"}},
		Flow: Flow{
			Entry:  "search",
			Output: "deliver",
			Nodes: []sdkr.FlowNode{
				{ID: "search", Kind: "tool", Tool: tool, Input: `{"query":"flight and hotel deals"}`, Output: "results"},
				{ID: "deliver", Kind: "tool", Tool: "channel.send", Input: `{"text":"{{ toJson .results }}"}`, Output: "sent"},
			},
			Edges: []sdkr.FlowEdge{{From: "search", To: "deliver"}},
		},
		Channels: []string{"telegram"},
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// scriptedLLM reproduces the reported behaviour: given the whole catalogue it
// reaches for web_search anyway, and only complies once the omission is named.
type scriptedLLM struct {
	prompts []string
}

func (s *scriptedLLM) Complete(ctx context.Context, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if strings.Contains(prompt, "CORRECTION") && strings.Contains(prompt, "mcp__trvl__travel") {
		return graphJSON("mcp__trvl__travel"), nil
	}
	return graphJSON("web_search"), nil
}

// The whole complaint in one test: the user named an installed MCP server, and
// the compiled workflow used a generic web search instead.
func TestWorkflowRetriesWhenItIgnoresANamedMCPServer(t *testing.T) {
	llm := &scriptedLLM{}
	res, err := RunGeneratePipeline(context.Background(), llm, trvlWorkflowIntent, trvlCatalog(), PipelineOptions{})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	tools := draftToolSet(res.Compile.Workflow)
	var usedTrvl, usedWebSearch bool
	for tool := range tools {
		switch strings.ToLower(tool) {
		case "mcp__trvl__travel":
			usedTrvl = true
		case "web_search":
			usedWebSearch = true
		}
	}
	if !usedTrvl {
		t.Errorf("the workflow does not call the trvl MCP tool the prompt named; tools = %v", tools)
	}
	if usedWebSearch {
		t.Errorf("the workflow still uses web_search in place of the named MCP tool; tools = %v", tools)
	}
	// The pipeline refines before it compiles, so the retry is the LAST prompt
	// rather than a fixed index.
	last := llm.prompts[len(llm.prompts)-1]
	if !strings.Contains(last, "CORRECTION") {
		t.Fatalf("no coverage retry was issued; %d model call(s) made", len(llm.prompts))
	}
	// The retry has to actually SAY what was missed — restating the same
	// catalogue would just produce the same answer again.
	if !strings.Contains(last, "mcp__trvl__travel") {
		t.Error("the retry prompt does not name the omitted capability")
	}
	if !strings.Contains(strings.ToLower(last), "do not substitute web_search") {
		t.Error("the retry prompt does not rule out the generic substitute that caused the failure")
	}
}

// No gap, no retry: this must not add a model call to every generation.
// The pipeline makes a refine call before the compile call, so a raw call count
// cannot distinguish "retried" from "normal". Detect the retry by its marker.
type countingLLM struct{ retried bool }

func (c *countingLLM) Complete(ctx context.Context, prompt string) (string, error) {
	if strings.Contains(prompt, "CORRECTION") {
		c.retried = true
	}
	return graphJSON("mcp__trvl__travel"), nil
}

func TestNoRetryWhenCoverageIsAlreadyComplete(t *testing.T) {
	llm := &countingLLM{}
	if _, err := RunGeneratePipeline(context.Background(), llm, trvlWorkflowIntent, trvlCatalog(), PipelineOptions{}); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if llm.retried {
		t.Error("retried a graph that already covered everything the prompt named")
	}
}

// A prompt naming nothing installed must not trigger a retry either — the guard
// keys on a CONCRETE omission, never on a vague topic guess.
func TestNoRetryWhenNothingInstalledWasNamed(t *testing.T) {
	llm := &countingLLM{}
	if _, err := RunGeneratePipeline(context.Background(), llm,
		"Every weekday at 7am send a news digest to telegram", trvlCatalog(), PipelineOptions{}); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if llm.retried {
		t.Error("retried for an intent that named nothing installed")
	}
}

// The deterministic planner had the same blind spot from the other direction:
// its search step was hardcoded to web_search and could not see MCP at all.
func TestDeterministicSearchStepPrefersANamedMCPTool(t *testing.T) {
	got := deterministicSearchTool(trvlWorkflowIntent, trvlCatalog())
	if got != "mcp__trvl__travel" {
		t.Errorf("deterministic search tool = %q, want the named MCP tool", got)
	}
	// And still falls back when the intent names nothing installed.
	if got := deterministicSearchTool("send a news digest every morning", trvlCatalog()); got != "web_search" {
		t.Errorf("fallback search tool = %q, want web_search", got)
	}
}
