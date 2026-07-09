package studio

import (
	"fmt"
	"testing"
)

// genSample is one raw "generated" draft plus whether the deterministic layer
// SHOULD be able to turn it into a valid, renderable flow.
type genSample struct {
	name        string
	raw         string
	recoverable bool // expected NormalizeAndCheck().Valid
}

// genCorpus captures the real failure classes seen in the field. Each entry that
// is `recoverable` must be repaired to validity by the deterministic pipeline
// alone (no LLM). The two unrecoverable entries prove the scorer still flags
// genuinely broken drafts (so a real regression can't hide as "100% valid").
var genCorpus = []genSample{
	{
		name:        "clean_agent",
		recoverable: true,
		raw: `{"name":"Echo","trigger":{"type":"channel"},"flow":{
			"nodes":[{"id":"a","kind":"agent","agent":"helper","input":"{{ .trigger.text }}","output":"reply"}],
			"edges":[{"from":"a","to":"end"}],"entry":"a"}}`,
	},
	{
		name:        "fromJson_in_input", // was: function "fromJson" not defined
		recoverable: true,
		raw: `{"name":"F","trigger":{"type":"channel"},"flow":{
			"nodes":[
			  {"id":"e","kind":"llm","input":"{{ .trigger.text }}","output":"params"},
			  {"id":"p","kind":"python","code":"def run(inputs):\n    return inputs","input":"{{ fromJson .params }}","output":"out"}],
			"edges":[{"from":"e","to":"p"},{"from":"p","to":"end"}],"entry":"e"}}`,
	},
	{
		name:        "start_node_kind", // was: unknown kind "start"
		recoverable: true,
		raw: `{"name":"S","trigger":{"type":"channel"},"flow":{
			"nodes":[
			  {"id":"start1","kind":"start"},
			  {"id":"a","kind":"agent","agent":"h","input":"{{ .trigger.text }}","output":"r"}],
			"edges":[{"from":"start1","to":"a"},{"from":"a","to":"end"}],"entry":"start1"}}`,
	},
	{
		name:        "trigger_message_ref", // .trigger.message alias
		recoverable: true,
		raw: `{"name":"M","trigger":{"type":"channel"},"flow":{
			"nodes":[{"id":"a","kind":"agent","agent":"h","input":"{\"q\": {{ toJson .trigger.message }}}","output":"r"}],
			"edges":[{"from":"a","to":"end"}],"entry":"a"}}`,
	},
	{
		name:        "channel_send_output", // output re-pointed to content node
		recoverable: true,
		raw: `{"name":"C","trigger":{"type":"channel"},"flow":{
			"nodes":[
			  {"id":"fmt","kind":"agent","agent":"f","input":"{{ .trigger.text }}","output":"msg"},
			  {"id":"send","kind":"tool","tool":"channel.send","input":"{\"text\": {{ toJson .msg }}}","output":"receipt"}],
			"edges":[{"from":"fmt","to":"send"},{"from":"send","to":"end"}],"entry":"fmt","output":"send"}}`,
	},
	{
		name:        "unclosed_template", // genuinely broken — must NOT validate
		recoverable: false,
		raw: `{"name":"B","trigger":{"type":"channel"},"flow":{
			"nodes":[{"id":"a","kind":"agent","agent":"h","input":"{{ .trigger.text ","output":"r"}],
			"edges":[{"from":"a","to":"end"}],"entry":"a"}}`,
	},
	{
		name:        "truly_unknown_kind", // must NOT validate
		recoverable: false,
		raw: `{"name":"K","trigger":{"type":"channel"},"flow":{
			"nodes":[{"id":"a","kind":"sqlquery"}],
			"edges":[{"from":"a","to":"end"}],"entry":"a"}}`,
	},
}

// TestGenerationRobustnessCorpus scores the deterministic pipeline over the
// corpus and reports the recovery rate. It fails if any recoverable draft is NOT
// repaired (a normalizer regression) or any unrecoverable draft slips through as
// valid (a scorer/validation regression).
func TestGenerationRobustnessCorpus(t *testing.T) {
	recoverableTotal, recovered := 0, 0
	for _, s := range genCorpus {
		got := NormalizeAndCheck(s.raw)
		if s.recoverable {
			recoverableTotal++
			if got.Valid {
				recovered++
			} else {
				t.Errorf("[%s] expected the deterministic layer to make it valid, got errors: %v", s.name, got.Errors)
			}
		} else if got.Valid {
			t.Errorf("[%s] expected an invalid draft to be flagged, but it passed", s.name)
		}
	}
	rate := 100 * float64(recovered) / float64(recoverableTotal)
	t.Logf("generation robustness: %d/%d recoverable drafts repaired deterministically (%.0f%%)",
		recovered, recoverableTotal, rate)
	if recovered != recoverableTotal {
		t.Fatalf("generation robustness regressed: %s", fmt.Sprintf("%d/%d", recovered, recoverableTotal))
	}
}
