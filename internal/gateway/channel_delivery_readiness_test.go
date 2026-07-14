package gateway

import (
	"net/http"
	"testing"
)

func TestChannelDeliveryReadinessListsDefaultAndBotTargets(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Channels = map[string]map[string]any{
		"telegram": {
			"enabled":           true,
			"token":             "tok",
			"default_output_to": "-10042",
			"bots": []any{
				map[string]any{
					"bot_name":          "Research Bot",
					"agent_id":          "research-librarian",
					"token":             "tok2",
					"default_output_to": "-10043",
				},
			},
		},
	}
	s.channels.Register(&fakeOpsAlertAdapter{id: "telegram", live: true})
	s.channels.Register(&fakeOpsAlertAdapter{id: "telegram-research-librarian", live: true})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels/delivery-readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delivery readiness = %d body=%v", status, body)
	}
	if body["status"] != "ok" || body["ready"] != float64(2) || body["total"] != float64(2) {
		t.Fatalf("unexpected readiness summary: %v", body)
	}
	targets, ok := body["targets"].([]any)
	if !ok || len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %v", body["targets"])
	}
}

func TestChannelDeliveryReadinessFlagsMissingDestination(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Channels = map[string]map[string]any{
		"slack": {
			"enabled": true,
			"token":   "tok",
		},
	}
	s.channels.Register(&fakeOpsAlertAdapter{id: "slack", live: true})

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/channels/delivery-readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("delivery readiness = %d body=%v", status, body)
	}
	if body["status"] != "fail" {
		t.Fatalf("expected fail for missing destination, got %v", body)
	}
}
