package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// studioFake registers a controllable LLM provider under the resolved studio
// provider id ("openai") so /studio/* endpoints that call the model work in a
// test. Returns the provider so the test can set its reply content.
func studioFake(t *testing.T) (*Server, *fakeLLMProvider) {
	t.Helper()
	s, _ := newTestGatewayWithLLM(t, "k")
	fake := &fakeLLMProvider{id: "openai"}
	s.llmRouter.Register(fake)
	return s, fake
}

// /studio/compile must return a reasoning AGENT (strategy set, no flow) for an
// intent with strong reasoning cues — the server-side authoritative routing
// guarantee. Regression for "it says agent but builds a brittle workflow".
func TestStudioCompile_RoutesReasoningTaskToAgent(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{
	  "name": "Finance QA",
	  "system_prompt": "Answer stock questions using the right finance skill.",
	  "trigger": {"type":"channel"},
	  "channels": ["http"],
	  "tools": ["web_search"],
	  "skills": [],
	  "rationale": "Dynamic skill routing."
	}`
	body := `{"intent":"An on-demand assistant that selects and calls the appropriate skill to answer stock questions","catalog":{}}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	wf, _ := out["workflow"].(map[string]any)
	if wf == nil {
		t.Fatalf("no workflow in response: %v", out)
	}
	if wf["strategy"] != "plan_execute" {
		t.Errorf("expected /compile to return a plan_execute agent, got strategy=%v", wf["strategy"])
	}
}

// Explicit Workflow mode is still protected for curated macro-workflow shapes:
// NotebookLM podcast generation should not fall through to LLM-authored graph
// JSON, which previously produced a useless one-node web_search workflow.
func TestStudioCompile_ForceWorkflowUsesDeterministicPodcastTemplate(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `not json`

	body := `{
	  "intent":"Every weekday at 7:00am, build an AI articles podcast as a fixed workflow. Sources: hbr.org, technologyreview.com, gartner.com. Deliver on telegram.",
	  "force_workflow":true,
	  "catalog":{
	    "tools":["web_search","channel.send"],
	    "channels":["telegram"],
	    "mcp":[{"server":"notebooklm","tools":[
	      {"name":"mcp__notebooklm__notebook_create","description":"Create notebook","params":"title*:string"},
	      {"name":"mcp__notebooklm__source_add","description":"Add source","params":"notebook_id*:string,source_type*:string,text:string,url:string,wait:boolean"},
	      {"name":"mcp__notebooklm__studio_create","description":"Create audio","params":"notebook_id*:string,artifact_type*:string,confirm:boolean"},
	      {"name":"mcp__notebooklm__studio_status","description":"Check status","params":"notebook_id*:string"}
	    ]}]
	  }
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/compile", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if got := fake.lastPrompt(); got != "" {
		t.Fatalf("deterministic workflow should not call the LLM compiler, got prompt %.120q", got)
	}
	wf, _ := out["workflow"].(map[string]any)
	if wf == nil {
		t.Fatalf("no workflow in response: %v", out)
	}
	flow, _ := wf["flow"].(map[string]any)
	nodes, _ := flow["nodes"].([]any)
	if len(nodes) < 6 {
		t.Fatalf("expected NotebookLM macro-flow, got %d nodes: %v", len(nodes), out)
	}
	raw, _ := json.Marshal(out)
	for _, want := range []string{"search_article_sources", "create_notebook", "add_article_sources", "generate_audio", "poll_audio_status"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("compiled workflow missing %s:\n%s", want, string(raw))
		}
	}
	if !strings.Contains(string(raw), `"max_parallel":3`) ||
		!strings.Contains(string(raw), `"item_var":"source_domain"`) ||
		!strings.Contains(string(raw), `"item_var":"article"`) {
		t.Fatalf("compiled workflow missing mapped search/source contracts:\n%s", string(raw))
	}
}

// /studio/try-agent runs an UNSAVED reasoning agent and returns its reply plus a
// (possibly empty) tool-call trace — without persisting the agent.
func TestStudioTryAgent_RunsAndReturnsReply(t *testing.T) {
	s, fake := studioFake(t)
	fake.content = `{"thought":"answer directly","is_done":true,"final_answer":"AAPL is up about 5% this quarter."}`

	body := `{"workflow":{"name":"QA","strategy":"react","system_prompt":"answer stock questions",` +
		`"trigger":{"type":"channel"},"channels":["http"],"tools":["web_search"]},"question":"how is AAPL doing?"}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/try-agent", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if reply, _ := out["reply"].(string); !strings.Contains(reply, "AAPL") {
		t.Errorf("expected the model reply echoed back, got %v", out["reply"])
	}
	if _, ok := out["trace"].([]any); !ok {
		t.Errorf("expected a trace array in the response, got %v", out["trace"])
	}
	// The ephemeral agent must NOT have been persisted.
	for _, d := range s.loader.All() {
		if d != nil && strings.HasPrefix(d.ID, "studio-try-") {
			t.Errorf("try-agent leaked a persisted agent: %s", d.ID)
		}
	}
}

// /studio/try-agent rejects a non-agent (fixed workflow) draft.
func TestStudioTryAgent_RejectsWorkflow(t *testing.T) {
	s, _ := studioFake(t)
	body := `{"workflow":{"name":"flow","flow":{"nodes":[]}},"question":"hi"}`
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/try-agent", "k", body)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 for a non-agent draft, got %d", status)
	}
}

// /studio/from-yaml warnings for a reasoning agent must NOT claim its system
// prompt / peers are lost (they round-trip), unlike the old inaccurate warnings.
func TestStudioFromYAML_ReasoningAgentWarningsAccurate(t *testing.T) {
	s, _ := studioFake(t)
	yaml := "id: qa\nname: QA\nsystem_prompt: answer stock questions\n" +
		"reasoning:\n  strategy: react\nagents:\n  - helper\n"
	body := `{"yaml":` + jsonQuote(yaml) + `}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/from-yaml", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	warns, _ := out["warnings"].([]any)
	for _, w := range warns {
		ws, _ := w.(string)
		low := strings.ToLower(ws)
		if strings.Contains(low, "regenerate") || strings.Contains(low, "aren't shown on the canvas") {
			t.Errorf("reasoning-agent from-yaml should not warn about lost prompt/peers; got %q", ws)
		}
	}
}

// jsonQuote returns a JSON-quoted string literal for embedding YAML in a body.
func jsonQuote(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n")
	return "\"" + r.Replace(s) + "\""
}
