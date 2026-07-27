package gateway

import "testing"

func TestProbeGenStream(t *testing.T) {
	for _, intent := range []string{
		"every morning read /tmp/sales.csv, summarize it and send it to telegram",
		"post a daily update to slack",
		"answer questions about stocks",
	} {
		s, fake := studioFake(t)
		fake.content = `{"refined_intent":"` + intent + `","summary":"x","assumptions":[],"questions":[]}`
		done := sseDoneFrame(t, s, "/api/v1/studio/generate/stream", `{"intent":"`+intent+`","auto_repair":true}`)
		res, _ := done["result"].(map[string]any)
		comp, _ := res["compile"].(map[string]any)
		ct, _ := comp["contract"].(map[string]any)
		t.Logf("intent=%q blocked=%v blockers=%v reason=%v", intent, done["blocked"], ct["blockers"], done["blocked_reason"])
	}
}
