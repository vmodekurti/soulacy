package gateway

import (
	"net/http"
	"testing"
)

func TestStudioContractEndpointReportsAuthoringRules(t *testing.T) {
	s, _ := studioFake(t)
	body := `{"workflow":{"name":"Brittle Flow","trigger":{"type":"manual"},"flow":{
	  "entry":"summarize",
	  "nodes":[
	    {"id":"summarize","kind":"agent","agent":"summarizer","output":"reply"},
	    {"id":"store","kind":"tool","tool":"kb_write","input":"please store it","output":"stored"}],
	  "edges":[{"from":"summarize","to":"store"}]}}}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/contract", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if _, ok := out["score"].(float64); !ok {
		t.Fatalf("contract response should include score: %v", out)
	}
	checks, _ := out["checks"].([]any)
	if len(checks) == 0 {
		t.Fatalf("contract response should include checks: %v", out)
	}
	found := false
	for _, raw := range checks {
		check, _ := raw.(map[string]any)
		if check["id"] == "data.contracts" && check["status"] == "warn" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected data.contracts warning in %v", checks)
	}
}

func TestStudioSaveRejectsContractBlockersBeforeCreatingAgent(t *testing.T) {
	s, _ := studioFake(t)
	before := len(s.loader.All())
	body := `{"workflow":{"name":"Born Broken","trigger":{"type":"manual"},"flow":{
	  "entry":"missing",
	  "nodes":[]}}}`

	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/save", "k", body)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if _, ok := out["contract"].(map[string]any); !ok {
		t.Fatalf("save failure should include contract details: %v", out)
	}
	if _, ok := out["preflight"].(map[string]any); !ok {
		t.Fatalf("save failure should include preflight details: %v", out)
	}
	if after := len(s.loader.All()); after != before {
		t.Fatalf("invalid Studio save created an agent: before=%d after=%d", before, after)
	}
}
