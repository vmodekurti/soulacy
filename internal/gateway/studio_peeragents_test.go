package gateway

// Peer agents: a workflow that delegates to an agent which does not exist yet
// must leave that agent behind when it is saved.
//
// Studio's generator is designed to invent helper agents — "summarizer",
// "notifier" — and declare them in the draft's new_agents. The contract is that
// saving materialises them. When it does not, the saved workflow references an
// agent that is not there and the run dies at the delegating node, with nothing
// in the UI to explain why.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// A one-node workflow whose only step delegates to an agent that does not exist.
const peerSaveBody = `{"workflow":{"name":"Travel Advisor","trigger":{"type":"manual"},
  "llm":{"provider":"openai","model":"gpt-4o-mini"},
  "new_agents":[{"id":"summarizer","name":"Summarizer","description":"Turns travel results into prose","system_prompt":"You are Summarizer. Turn structured travel results into a short, friendly recommendation for the traveller. If the input is empty, say so plainly."}],
  "flow":{
  "entry":"format_response",
  "nodes":[
    {"id":"format_response","kind":"agent","agent":"summarizer","input":"Summarise the traveller request: {{ .trigger.text }}","output":"response"}],
  "edges":[]}}}`

func agentIDs(s *Server) []string {
	out := []string{}
	for _, a := range s.loader.All() {
		out = append(out, a.ID)
	}
	return out
}

func TestStudioSave_CreatesMissingPeerAgent(t *testing.T) {
	s, _ := studioFake(t)
	if existing := s.loader.Get("summarizer"); existing != nil {
		t.Fatal("precondition: summarizer should not exist")
	}

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", peerSaveBody)
	if status != http.StatusCreated {
		t.Fatalf("save status=%d body=%v", status, out)
	}

	peer := s.loader.Get("summarizer")
	if peer == nil {
		t.Fatalf("peer agent 'summarizer' was not created; agents now: %v", agentIDs(s))
	}
	if peer.SourcePath == "" {
		t.Fatal("peer agent exists only in memory (no SourcePath) — it would vanish on restart")
	}
	if peer.SystemPrompt == "" {
		t.Fatal("peer agent was created blank")
	}
	// The user should be told their agent list just grew.
	reported, _ := out["peerAgents"].([]any)
	if len(reported) != 1 || reported[0] != "summarizer" {
		t.Fatalf("save should report the helper agents it created, got %v", out["peerAgents"])
	}
}

// The code view saves the SAME workflow as the wizard. When only one of the two
// paths materialised peers, which button you pressed decided whether the agent
// you just saved could run.
func TestStudioSaveYAML_CreatesMissingPeerAgent(t *testing.T) {
	s, _ := studioFake(t)
	if existing := s.loader.Get("summarizer"); existing != nil {
		t.Fatal("precondition: summarizer should not exist")
	}

	yamlDoc := `id: travel-advisor-agent
name: Travel Advisor
enabled: true
llm:
  provider: openai
  model: gpt-4o-mini
workflow:
  entry: format_response
  nodes:
    - id: format_response
      kind: agent
      agent: summarizer
      input: "Summarise: {{ .trigger.text }}"
      output: response
`
	raw, err := json.Marshal(map[string]any{"yaml": yamlDoc})
	if err != nil {
		t.Fatal(err)
	}
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save-yaml", "k", string(raw))
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("save-yaml status=%d body=%v", status, out)
	}

	peer := s.loader.Get("summarizer")
	if peer == nil {
		t.Fatalf("peer agent 'summarizer' was not created; agents now: %v", agentIDs(s))
	}
	// No draft on this path, so the profile has to come from synthesis.
	if peer.SystemPrompt == "" {
		t.Fatal("peer agent was created blank on the YAML path")
	}
	if reported, _ := out["peerAgents"].([]any); len(reported) != 1 {
		t.Fatalf("save-yaml should report created helpers, got %v", out["peerAgents"])
	}
}

// Materialising peers must never overwrite an agent the user already owns.
func TestStudioSave_DoesNotClobberExistingAgent(t *testing.T) {
	s, _ := studioFake(t)
	original := &agent.Definition{
		ID: "summarizer", Name: "My Summarizer",
		SystemPrompt: "Hand-written prompt the user cares about.",
		Enabled:      true,
	}
	if err := s.loader.Upsert(t.TempDir(), original); err != nil {
		t.Fatalf("seed: %v", err)
	}

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", peerSaveBody)
	if status != http.StatusCreated {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if got := s.loader.Get("summarizer").SystemPrompt; got != "Hand-written prompt the user cares about." {
		t.Fatalf("existing agent was overwritten by a synthesized stub: %q", got)
	}
	if reported, _ := out["peerAgents"].([]any); len(reported) != 0 {
		t.Fatalf("nothing was created, so nothing should be reported: %v", reported)
	}
}

// Saving the same workflow twice must not report a second creation.
func TestStudioSave_PeerCreationIsIdempotent(t *testing.T) {
	s, _ := studioFake(t)
	if status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", peerSaveBody); status != http.StatusCreated {
		t.Fatalf("first save status=%d body=%v", status, out)
	}
	_, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", peerSaveBody)
	if reported, _ := out["peerAgents"].([]any); len(reported) != 0 {
		t.Fatalf("second save re-created the peer: %v", reported)
	}
}
