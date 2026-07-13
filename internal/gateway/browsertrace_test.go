package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

func TestBrowserTraceIncludesAgentNetworkPolicy(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.loader.Register(&agent.Definition{
		ID:      "browser-agent",
		Name:    "Browser Agent",
		Enabled: true,
		Policy: agent.ToolPolicyConfig{
			Enabled:      true,
			Network:      "prompt",
			AllowDomains: []string{"example.com", "docs.internal"},
			DenyDomains:  []string{"ads.example.com"},
		},
	})
	s.actions = &fakeTailBackend{events: []message.Event{{
		Type:      "tool.call",
		AgentID:   "browser-agent",
		SessionID: "sess-browser",
		Timestamp: time.Now().UTC(),
		Payload: message.ToolCall{
			Name:      "mcp__playwright__browser_navigate",
			Arguments: map[string]any{"url": "https://example.com/docs"},
		},
	}}}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/browser/trace?agent_id=browser-agent&session_id=sess-browser", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	policy, ok := body["policy"].(map[string]any)
	if !ok {
		t.Fatalf("missing policy summary: %#v", body)
	}
	if policy["browser_action"] != "prompt" || policy["requires_approval"] != true {
		t.Fatalf("unexpected policy summary: %#v", policy)
	}
	allow, _ := policy["allow_domains"].([]any)
	if len(allow) != 2 || allow[0] != "example.com" {
		t.Fatalf("allow domains not preserved: %#v", policy)
	}
	trace, _ := body["trace"].(map[string]any)
	if trace["last_url"] != "https://example.com/docs" {
		t.Fatalf("trace missing browser event: %#v", trace)
	}
}
