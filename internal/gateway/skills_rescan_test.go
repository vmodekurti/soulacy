package gateway

// Story E18: POST /api/v1/skills/rescan hot-loads freshly installed skills.

import (
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/pkg/skill"
)

// rescanSkillLoader satisfies runtime.SkillLoader plus the Scan() extension
// the handler type-asserts on.
type rescanSkillLoader struct {
	scanned int
	skills  []*skill.Skill
}

func (l *rescanSkillLoader) BuildCatalog() string    { return "" }
func (l *rescanSkillLoader) Get(string) *skill.Skill { return nil }
func (l *rescanSkillLoader) All() []*skill.Skill     { return l.skills }
func (l *rescanSkillLoader) Scan() []error           { l.scanned++; return nil }

func TestSkillsRescan(t *testing.T) {
	srv := newTestGateway(t, "secret")
	loader := &rescanSkillLoader{skills: []*skill.Skill{{Name: "a"}, {Name: "b"}}}
	srv.skillLoader = loader

	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/skills/rescan", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if loader.scanned != 1 {
		t.Errorf("Scan called %d times, want 1", loader.scanned)
	}
	if int(body["count"].(float64)) != 2 {
		t.Errorf("count = %v, want 2", body["count"])
	}
}

func TestSkillsRescan_NoLoader(t *testing.T) {
	srv := newTestGateway(t, "secret")
	status, _ := gatewayJSON(t, srv, http.MethodPost, "/api/v1/skills/rescan", "secret", "")
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
}
