package gateway

import (
	"net/http"
	"strings"
	"testing"
)

// /studio/repair-live turns a live node trace into repair proposals. Here the
// producer returned its list under "items" but the formatter node read
// ".search.results" — the deterministic layer should propose a remap with no LLM.
func TestStudioRepairLive_DeterministicRemap(t *testing.T) {
	s, _ := studioFake(t)
	body := `{
	  "workflow": {"name":"News","trigger":{"type":"channel"},"flow":{
	    "nodes":[
	      {"id":"search","kind":"tool","tool":"web_search","output":"search"},
	      {"id":"fmt","kind":"agent","agent":"writer","input":"{{ toJson .search.results }}","output":"reply"}],
	    "edges":[{"from":"search","to":"fmt"},{"from":"fmt","to":"end"}],"entry":"search"}},
	  "node_trace":[
	    {"node_id":"search","kind":"tool","output":"{\"items\":[{\"title\":\"a\"}],\"meta\":{\"n\":1}}"},
	    {"node_id":"fmt","kind":"agent","input":"{{ toJson .search.results }}","error":"can't evaluate field results in type interface"}]
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/repair-live", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	props, _ := out["proposals"].([]any)
	if len(props) != 1 {
		t.Fatalf("want 1 proposal, got %v", out["proposals"])
	}
	p := props[0].(map[string]any)
	if p["node_id"] != "fmt" {
		t.Fatalf("wrong node: %v", p)
	}
	if nw, _ := p["new"].(string); !strings.Contains(nw, ".search.items") {
		t.Fatalf("expected remap to .search.items, got %q", nw)
	}
	if auto, _ := p["auto"].(bool); !auto {
		t.Error("deterministic remap should be auto")
	}
}

// /studio/apply-repair applies one approved proposal and re-validates.
func TestStudioApplyRepair_AppliesAndValidates(t *testing.T) {
	s, _ := studioFake(t)
	body := `{
	  "workflow": {"name":"News","trigger":{"type":"channel"},"flow":{
	    "nodes":[
	      {"id":"fmt","kind":"agent","agent":"writer","input":"{{ toJson .search.results }}","output":"reply"}],
	    "edges":[{"from":"fmt","to":"end"}],"entry":"fmt"}},
	  "proposal": {"node_id":"fmt","field":"input","new":"{{ toJson .search.items }}"}
	}`
	status, out := gatewayJSON(t, s, http.MethodPost, "/api/v1/studio/apply-repair", "k", body)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if valid, _ := out["valid"].(bool); !valid {
		t.Fatalf("patched draft should be valid, got %v", out["errors"])
	}
	wf, _ := out["workflow"].(map[string]any)
	flow, _ := wf["flow"].(map[string]any)
	nodes, _ := flow["nodes"].([]any)
	n0 := nodes[0].(map[string]any)
	if in, _ := n0["input"].(string); !strings.Contains(in, ".search.items") {
		t.Fatalf("node input not patched: %q", in)
	}
}
