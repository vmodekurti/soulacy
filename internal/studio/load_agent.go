// load_agent.go — the reverse of save.go's ToAgentDefinition: turn a saved
// agent.Definition (one that carries a workflow graph) back into a Studio Draft
// so it can be re-opened, edited on the canvas, and re-saved. Powers Studio's
// "My Workflows" → Edit. Pure mapping (no I/O) so it is unit-testable.
package studio

import (
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// HasWorkflow reports whether an agent definition carries a graph workflow
// (i.e. it was authored in Studio, or is otherwise editable as a flow). Agents
// without a Workflow are skipped by the "My Workflows" list.
func HasWorkflow(def agent.Definition) bool {
	return def.Workflow != nil && len(def.Workflow.Nodes) > 0
}

// FromAgentDefinition maps a saved agent.Definition back into a Studio Draft.
// It is the inverse of ToAgentDefinition for the fields Studio owns: name,
// trigger (+ cron), channels, and the flow graph. Fields the agent gained
// outside Studio (system prompt, labels, …) are intentionally dropped — Studio
// edits the WORKFLOW, and a re-save regenerates those.
func FromAgentDefinition(def agent.Definition) Draft {
	d := Draft{
		Name:     def.Name,
		Intent:   def.StudioIntent,
		Refined:  def.StudioRefined,
		Trigger:  Trigger{Type: triggerTypeFromKind(def.Trigger)},
		Channels: append([]string(nil), def.Channels...),
	}
	if d.Name == "" {
		d.Name = def.ID
	}
	if def.Schedule != nil && strings.TrimSpace(def.Schedule.Cron) != "" {
		d.Trigger.Config = map[string]any{"cron": def.Schedule.Cron}
	}
	if def.Workflow != nil {
		d.Flow = Flow{
			Nodes:  append(def.Workflow.Nodes[:0:0], def.Workflow.Nodes...),
			Edges:  append(def.Workflow.Edges[:0:0], def.Workflow.Edges...),
			Entry:  def.Workflow.Entry,
			Output: def.Workflow.Output,
		}
	}
	return d
}

// triggerTypeFromKind is the inverse of mapTrigger: agent TriggerKind → the
// Studio trigger.type vocabulary (schedule | channel | webhook | manual).
func triggerTypeFromKind(k agent.TriggerKind) string {
	switch k {
	case agent.TriggerCron:
		return "schedule"
	case agent.TriggerChannel:
		return "channel"
	case agent.TriggerWebhook:
		return "webhook"
	case agent.TriggerInternal:
		return "manual"
	default:
		return "manual"
	}
}
