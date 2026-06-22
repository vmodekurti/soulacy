package studio

import (
	"context"
	"testing"
)

func TestRefinePrompt_StrongCuesOverrideModelWorkflow(t *testing.T) {
	// Model insists "workflow" but the task is a NotebookLM audio job → react.
	out := `{"refined_intent":"Daily at 7am, authenticate with NotebookLM, create a notebook, add each source, generate the audio overview and poll status until ready, then deliver.","summary":"daily audio briefing","recommended_mode":"workflow","mode_reason":"fixed daily sequence"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "daily ai audio news briefing with notebooklm", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode != "react" {
		t.Errorf("strong async/notebooklm cues must override to react, got %q", r.RecommendedMode)
	}
}

func TestRefinePrompt_PlainWorkflowStaysWorkflow(t *testing.T) {
	out := `{"refined_intent":"Every weekday at 8am search the web for AI news, summarize the top 5, post to Telegram.","summary":"daily digest","recommended_mode":"workflow","mode_reason":"fixed pipeline"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "daily ai news digest to telegram", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode != "workflow" {
		t.Errorf("a plain fixed pipeline should stay workflow, got %q", r.RecommendedMode)
	}
}

func TestHasStrongReactCues(t *testing.T) {
	if !hasStrongReactCues("add each source then poll until ready") {
		t.Error("expected strong cues")
	}
	if hasStrongReactCues("search and summarize and post") {
		t.Error("plain pipeline should have no strong cues")
	}
}
