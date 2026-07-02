package gateway

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/message"
)

type scheduleOutputTestAdapter struct {
	mu   sync.Mutex
	sent []message.Message
}

func (a *scheduleOutputTestAdapter) ID() string   { return "test-output" }
func (a *scheduleOutputTestAdapter) Name() string { return "Test Output" }
func (a *scheduleOutputTestAdapter) Start(context.Context, chan<- message.Message) error {
	return nil
}
func (a *scheduleOutputTestAdapter) Send(_ context.Context, msg message.Message) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, msg)
	return nil
}
func (a *scheduleOutputTestAdapter) Stop() error { return nil }
func (a *scheduleOutputTestAdapter) Status() channels.AdapterStatus {
	return channels.AdapterStatus{Connected: true, Detail: "test"}
}
func (a *scheduleOutputTestAdapter) last() (message.Message, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sent) == 0 {
		return message.Message{}, false
	}
	return a.sent[len(a.sent)-1], true
}

func TestGatewayHandleTestScheduledOutput_SendsViaConfiguredChannel(t *testing.T) {
	s := newTestGateway(t, "secret")
	adp := &scheduleOutputTestAdapter{}
	s.channels.Register(adp)

	body := `{
		"id":"sched-output-agent",
		"name":"Schedule Output Agent",
		"trigger":"cron",
		"channels":["http"],
		"enabled":true,
		"schedule":{
			"cron":"0 7 * * *",
			"output":{
				"channel":"test-output",
				"to":"chat-123",
				"bot_name":"QA Bot",
				"template":"[TEST {trigger}] {agent_id}: {reply}"
			}
		},
		"llm":{"provider":"test","model":"fake-model"},
		"system_prompt":"hello"
	}`
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", body)
	if status != http.StatusCreated {
		t.Fatalf("create agent status = %d", status)
	}

	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/sched-output-agent/schedule-output/test", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("test output status = %d body=%v", status, res)
	}
	if res["channel"] != "test-output" || res["to"] != "chat-123" {
		t.Fatalf("unexpected response: %v", res)
	}

	msg, ok := adp.last()
	if !ok {
		t.Fatal("adapter did not receive a message")
	}
	if msg.Channel != "test-output" || msg.ThreadID != "chat-123" || msg.AgentID != "sched-output-agent" {
		t.Fatalf("unexpected outbound message: %+v", msg)
	}
	text := strings.Join(func() []string {
		var out []string
		for _, p := range msg.Parts {
			if p.Type == message.ContentText {
				out = append(out, p.Text)
			}
		}
		return out
	}(), "\n")
	if !strings.Contains(text, "[TEST test_output] sched-output-agent: Soulacy scheduled-output test") {
		t.Fatalf("unexpected outbound text: %q", text)
	}
}

func TestGatewayHandleTestScheduledOutput_RejectsUnregisteredChannel(t *testing.T) {
	s := newTestGateway(t, "secret")
	body := `{
		"id":"sched-output-missing-channel",
		"name":"Schedule Output Missing Channel",
		"trigger":"cron",
		"channels":["http"],
		"enabled":true,
		"schedule":{"cron":"0 7 * * *","output":{"channel":"missing-output","to":"chat-123"}},
		"llm":{"provider":"test","model":"fake-model"},
		"system_prompt":"hello"
	}`
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents", "secret", body)
	if status != http.StatusCreated {
		t.Fatalf("create agent status = %d", status)
	}
	status, res := gatewayJSON(t, s, http.MethodPost, "/api/v1/agents/sched-output-missing-channel/schedule-output/test", "secret", "")
	if status != http.StatusBadRequest {
		t.Fatalf("test output status = %d body=%v", status, res)
	}
	if !strings.Contains(res["error"].(string), "not registered") {
		t.Fatalf("unexpected error: %v", res)
	}
}
