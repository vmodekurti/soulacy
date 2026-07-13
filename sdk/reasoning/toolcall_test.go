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
