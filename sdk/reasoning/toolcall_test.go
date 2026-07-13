package reasoning

import (
	"encoding/json"
	"testing"
)

func TestToolCallUnmarshalAcceptsToolNameAndParametersAliases(t *testing.T) {
	var call ToolCall
	if err := json.Unmarshal([]byte(`{"tool_name":"fetch_url","parameters":{"url":"https://example.com","depth":2}}`), &call); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if call.Tool != "fetch_url" {
		t.Fatalf("Tool = %q, want fetch_url", call.Tool)
	}
	if call.Input["url"] != "https://example.com" || call.Input["depth"] != "2" {
		t.Fatalf("Input aliases not normalized: %#v", call.Input)
	}
	if call.Arguments["depth"] != float64(2) {
		t.Fatalf("Arguments lost numeric value: %#v", call.Arguments)
	}
}

func TestToolCallUnmarshalAcceptsNameParamsAndActionInputAliases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"name+params", `{"name":"queue_list","params":{"queue":"pending_resources"}}`},
		{"tool+action_input", `{"tool":"queue_list","action_input":{"queue":"pending_resources"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var call ToolCall
			if err := json.Unmarshal([]byte(tc.raw), &call); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if call.Tool != "queue_list" || call.Input["queue"] != "pending_resources" {
				t.Fatalf("unexpected call: %#v", call)
			}
		})
	}
}

func TestPlannedStepUnmarshalAcceptsArgumentAliases(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"arguments", `{"id":"s1","description":"send","tool":"channel.send","arguments":{"channel":"telegram","to":123,"text":"hi"}}`},
		{"input", `{"id":"s1","description":"send","tool":"channel.send","input":{"channel":"telegram","to":123,"text":"hi"}}`},
		{"params", `{"id":"s1","description":"send","tool":"channel.send","params":{"channel":"telegram","to":123,"text":"hi"}}`},
		{"parameters", `{"id":"s1","description":"send","tool":"channel.send","parameters":{"channel":"telegram","to":123,"text":"hi"}}`},
		{"action_input", `{"id":"s1","description":"send","tool":"channel.send","action_input":{"channel":"telegram","to":123,"text":"hi"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var step PlannedStep
			if err := json.Unmarshal([]byte(tc.raw), &step); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if step.Tool != "channel.send" || step.Arguments["channel"] != "telegram" || step.Input["to"] != "123" {
				t.Fatalf("step arguments not normalized: %#v", step)
			}
		})
	}
}
