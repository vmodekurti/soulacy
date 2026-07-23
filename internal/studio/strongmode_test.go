package studio

import (
	"context"
	"testing"
)

func TestRefinePrompt_StrongCuesOverrideModelWorkflow(t *testing.T) {
	// Model insists "workflow" but the task is a NotebookLM audio job →
	// plan_execute. ReAct is now an explicit/manual strategy, not Studio's
	// automatic escape hatch.
	out := `{"refined_intent":"Daily at 7am, authenticate with NotebookLM, create a notebook, add each source, generate the audio overview and poll status until ready, then deliver.","summary":"daily audio briefing","recommended_mode":"workflow","mode_reason":"fixed daily sequence"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "daily ai audio news briefing with notebooklm", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode != "plan_execute" {
		t.Errorf("strong async/notebooklm cues must override to plan_execute, got %q", r.RecommendedMode)
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

func TestRefinePrompt_PreservesAutoRecommendation(t *testing.T) {
	out := `{"refined_intent":"Create an interactive weather assistant that answers user weather questions by choosing the right weather tool at runtime.","summary":"interactive weather assistant","recommended_mode":"auto","mode_reason":"ordinary tool-calling agent"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "weather assistant for user questions", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode != "auto" {
		t.Errorf("auto recommendation should survive refine, got %q", r.RecommendedMode)
	}
}

func TestRefinePrompt_DemotesImplicitReactRecommendation(t *testing.T) {
	out := `{"refined_intent":"Research SNDK prospects using finance tools and web search, then synthesize the answer.","summary":"stock research","recommended_mode":"react","mode_reason":"dynamic tool use"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "Tell me about SNDK prospects", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode == "react" {
		t.Fatalf("implicit model ReAct recommendation should be demoted; got %q", r.RecommendedMode)
	}
	if r.RecommendedMode != "plan_execute" {
		t.Fatalf("implicit ReAct should become plan_execute for adaptive research; got %q", r.RecommendedMode)
	}
}

func TestRefinePrompt_AllowsExplicitReactRequest(t *testing.T) {
	out := `{"refined_intent":"Build a classic ReAct agent that loops through thought, action, and observation while researching.","summary":"react experiment","recommended_mode":"workflow","mode_reason":"x"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "Build a ReAct stock research agent", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode != "react" {
		t.Fatalf("explicit ReAct request should remain react; got %q", r.RecommendedMode)
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

// The finance-assistant pattern — an interactive agent that maps a question to
// the appropriate skill — must classify as an agent, not a workflow. It should
// no longer become ReAct by default; Studio should use Plan-Execute/Auto unless
// the user explicitly asks for ReAct.
func TestRecommendAgentMode_SkillRoutingAssistant(t *testing.T) {
	intent := `An interactive, on-demand financial assistant that responds to user questions ` +
		`about stocks and markets. Based on the parsed intent, it selects and calls the ` +
		`appropriate skill(s) from the deployed finance catalog.`
	if got := RecommendAgentMode(intent); got != "plan_execute" {
		t.Fatalf("a dynamic skill-routing assistant must be plan_execute by default; got %q", got)
	}
	// A genuinely fixed pipeline must NOT be forced to an agent.
	fixed := "Every weekday at 8am, search the web for AI news, summarize the top 5, and post to Telegram."
	if got := RecommendAgentMode(fixed); got != "" {
		t.Errorf("a fixed scheduled pipeline should stay a workflow; got %q", got)
	}
}

// Plan-Execute is now deterministically reachable, and is preferred over react
// when the intent explicitly asks to plan-then-execute a multi-phase job.
func TestRecommendAgentMode_PlanExecute(t *testing.T) {
	if got := RecommendAgentMode("First plan the research, decompose it into steps, then execute the plan and write a report"); got != "plan_execute" {
		t.Fatalf("an explicit decompose-then-execute task should be plan_execute; got %q", got)
	}
	// Refine path surfaces it too (deterministic override beats a model 'workflow').
	out := `{"refined_intent":"Devise a plan to research three vendors, then execute it step by step.","summary":"vendor research","recommended_mode":"workflow","mode_reason":"x"}`
	r, err := RefinePrompt(context.Background(), fakeLLM{out: out}, "research three cloud vendors with a plan", Catalog{})
	if err != nil {
		t.Fatalf("RefinePrompt: %v", err)
	}
	if r.RecommendedMode != "plan_execute" {
		t.Errorf("refine should surface plan_execute from strong cues; got %q", r.RecommendedMode)
	}
}
