package gateway

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/pkg/message"
)

const adminAuditAgentID = "_system"

type adminAuditRecord struct {
	Timestamp time.Time      `json:"timestamp"`
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	Target    string         `json:"target,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	Role      string         `json:"role,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Status    string         `json:"status"`
	Details   map[string]any `json:"details,omitempty"`
}

func (s *Server) recordAdminAudit(c *fiber.Ctx, action, resource, target, status string, details map[string]any) {
	if s == nil || s.actions == nil {
		return
	}
	rec := adminAuditRecord{
		Timestamp: time.Now().UTC(),
		Action:    strings.TrimSpace(action),
		Resource:  strings.TrimSpace(resource),
		Target:    strings.TrimSpace(target),
		Status:    strings.TrimSpace(status),
		Details:   scrubAuditDetails(details),
	}
	if rec.Status == "" {
		rec.Status = "ok"
	}
	if c != nil {
		if requestID, ok := c.Locals("request_id").(string); ok {
			rec.RequestID = requestID
		}
		if claims := auth.ClaimsFromCtx(c); claims != nil {
			rec.Actor = strings.TrimSpace(claims.Subject)
			if rec.Actor == "" {
				rec.Actor = strings.TrimSpace(claims.Email)
			}
			rec.Role = strings.TrimSpace(claims.Role)
		}
	}
	if rec.Actor == "" {
		rec.Actor = "api-key"
	}
	s.actions.Append(message.Event{
		Type:      "admin.audit",
		AgentID:   adminAuditAgentID,
		SessionID: rec.RequestID,
		Timestamp: rec.Timestamp,
		Payload:   rec,
	})
}

func adminAuditEventTypes() map[string]bool {
	return map[string]bool{"admin.audit": true}
}

func (s *Server) handleAdminAudit(c *fiber.Ctx) error {
	if s.actions == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "admin audit not available (action log disabled)")
	}
	q, ok := s.actions.(eventQuerier)
	if !ok {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "admin audit requires a durable action log backend")
	}
	limit := c.QueryInt("limit", 100)
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	events, err := q.QueryEvents(adminAuditAgentID, "", limit, adminAuditEventTypes())
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	records := make([]adminAuditRecord, 0, len(events))
	for _, ev := range events {
		rec, ok := adminAuditRecordFromPayload(ev.Payload)
		if !ok {
			continue
		}
		if rec.Timestamp.IsZero() {
			rec.Timestamp = ev.Timestamp
		}
		records = append(records, rec)
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].Timestamp.After(records[j].Timestamp)
	})
	return c.JSON(fiber.Map{
		"available": true,
		"source":    "action-log",
		"count":     len(records),
		"events":    records,
	})
}

func adminAuditRecordFromPayload(payload any) (adminAuditRecord, bool) {
	switch v := payload.(type) {
	case adminAuditRecord:
		return v, true
	case *adminAuditRecord:
		if v == nil {
			return adminAuditRecord{}, false
		}
		return *v, true
	case map[string]any:
		var rec adminAuditRecord
		raw, err := json.Marshal(v)
		if err != nil {
			return adminAuditRecord{}, false
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return adminAuditRecord{}, false
		}
		return rec, true
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return adminAuditRecord{}, false
		}
		var rec adminAuditRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return adminAuditRecord{}, false
		}
		return rec, rec.Action != "" || rec.Resource != ""
	}
}

func scrubAuditDetails(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") {
			out[key] = "***"
			continue
		}
		out[key] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func configPatchSections(p PatchableConfig) []string {
	sections := make([]string, 0, 12)
	if p.Server != nil {
		sections = append(sections, "server")
	}
	if p.Runtime != nil {
		sections = append(sections, "runtime")
	}
	if p.Executor != nil {
		sections = append(sections, "executor")
	}
	if p.LLM != nil {
		sections = append(sections, "llm")
	}
	if p.Log != nil {
		sections = append(sections, "log")
	}
	if p.Search != nil {
		sections = append(sections, "search")
	}
	if p.Costs != nil {
		sections = append(sections, "costs")
	}
	if p.Ops != nil {
		sections = append(sections, "ops")
	}
	if p.Deployment != nil {
		sections = append(sections, "deployment")
	}
	if p.AgentDirs != nil {
		sections = append(sections, "agent_dirs")
	}
	if p.SkillDirs != nil {
		sections = append(sections, "skill_dirs")
	}
	if p.PluginsConfig != nil {
		sections = append(sections, "plugins_config")
	}
	return sections
}

func channelSettingKeys(settings map[string]any, spec *channelSpec) []string {
	if len(settings) == 0 {
		return nil
	}
	secret := map[string]bool{}
	if spec != nil {
		for _, f := range spec.Fields {
			if f.Secret {
				secret[f.Key] = true
			}
		}
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		label := k
		if secret[k] {
			label = k + " (secret)"
		}
		keys = append(keys, label)
	}
	sort.Strings(keys)
	return keys
}

func auditStringList(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return strings.Join(v, ",")
}

func auditBoolPtrSet(v *bool) bool {
	return v != nil
}

func auditCount(v any) int {
	switch x := v.(type) {
	case []map[string]any:
		return len(x)
	case []any:
		return len(x)
	default:
		return 0
	}
}

func auditFmt(v any) string {
	return strings.TrimSpace(fmt.Sprint(v))
}
