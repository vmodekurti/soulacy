package gateway

import (
	"net/http"
	"testing"
)

func TestBrowserStatusEndpointReturnsChecks(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/browser/status", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("browser status = %d body=%v", status, body)
	}
	checks, ok := body["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("missing checks: %#v", body)
	}
	if _, ok := body["score"].(float64); !ok {
		t.Fatalf("missing score: %#v", body)
	}
}

func TestReadinessIncludesBrowserAutomationPosture(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d body=%v", status, body)
	}
	if _, ok := body["browser"].(map[string]any); !ok {
		t.Fatalf("missing browser readiness: %#v", body)
	}
	parity := body["parity"].(map[string]any)
	areas := parity["areas"].([]any)
	for _, raw := range areas {
		area := raw.(map[string]any)
		if area["key"] == "browser" {
			if got, _ := area["score"].(float64); got <= 0 {
				t.Fatalf("browser score = %v", area["score"])
			}
			return
		}
	}
	t.Fatalf("browser parity area missing: %#v", areas)
}

func TestParityBrowserAutomationScoresReadyPosture(t *testing.T) {
	area := parityBrowserAutomation(browserAutomationReadiness{Status: "ok", Score: 100})
	if area.Status != "ok" || area.Score < 82 {
		t.Fatalf("browser parity = %#v, want ok >= 82", area)
	}

	area = parityBrowserAutomation(browserAutomationReadiness{
		Status: "warn",
		Score:  40,
		NextActions: []executorAction{{
			Detail: "Add Browser headless from MCP.",
		}},
	})
	if area.Status != "fail" || area.Next == "" {
		t.Fatalf("browser parity = %#v, want fail with next action", area)
	}
}
