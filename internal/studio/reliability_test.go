package studio

import (
	"strings"
	"testing"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"
)

func TestValidateBlocksSearchOnlyWhenIntentNeedsFinishedArtifact(t *testing.T) {
	d := Draft{
		Intent:   "Find recent AI articles and create a podcast briefing from them",
		Trigger:  Trigger{Type: "manual"},
		Channels: []string{"telegram"},
		Flow: Flow{
			Nodes: []sdkr.FlowNode{
				{ID: "search_articles", Kind: "tool", Tool: "web_search", Input: `{"query":"AI articles"}`, Output: "results"},
			},
			Entry:  "search_articles",
			Output: "search_articles",
		},
	}
	got := Validate(d)
	if got.Ok {
		t.Fatalf("search-only graph should be blocked for finished-artifact intent: %+v", got)
	}
	if !validateHasError(got, "discovery/search") {
		t.Fatalf("expected completion-contract error, got %+v", got.Errors)
	}
}

func TestReactSystemPromptIncludesCompletionContractForMultiStepAgent(t *testing.T) {
	d := Draft{
		Name:         "Research Librarian",
		Strategy:     "auto",
		Intent:       "Search URLs, store tagged summaries in the Knowledge base, and send a Slack notification",
		SystemPrompt: "You are a careful research librarian who processes sources.",
		Tools:        []string{"web_search", "kb_write", "channel.send"},
		Channels:     []string{"slack"},
	}
	got := reactSystemPrompt(d)
	if !strings.Contains(got, completionContractHeading) {
		t.Fatalf("prompt missing completion contract:\n%s", got)
	}
	if !strings.Contains(got, "Raw tool JSON") {
		t.Fatalf("completion contract should reject raw tool JSON final answers:\n%s", got)
	}
}

func TestReasoningAgentCarriesScheduleOutputWhenFullyConfigured(t *testing.T) {
	d := Draft{
		Name:         "Morning Digest",
		Strategy:     "auto",
		Intent:       "Send a daily digest to Telegram",
		SystemPrompt: "You produce a useful daily digest and deliver it to the configured channel.",
		Trigger:      Trigger{Type: "schedule", Config: map[string]any{"cron": "0 7 * * *"}},
		Channels:     []string{"telegram"},
		Output:       &ScheduleOutput{Channel: "telegram", To: "12345", BotName: "Digest Bot"},
		Tools:        []string{"web_search", "channel.send"},
	}
	def, err := ToAgentDefinition(d, false)
	if err != nil {
		t.Fatalf("ToAgentDefinition: %v", err)
	}
	if def.Schedule == nil || def.Schedule.Output == nil {
		t.Fatalf("expected schedule output to be preserved: %+v", def.Schedule)
	}
	if def.Schedule.Output.Channel != "telegram" || def.Schedule.Output.To != "12345" {
		t.Fatalf("unexpected output: %+v", def.Schedule.Output)
	}
}

func validateHasError(res ValidateResult, want string) bool {
	for _, e := range res.Errors {
		if strings.Contains(e.Message, want) {
			return true
		}
	}
	return false
}
