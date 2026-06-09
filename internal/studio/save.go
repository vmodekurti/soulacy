// save.go — Studio's "save" step (Story S1.x, Wave 2). It converts a draft
// workflow into a Soulacy agent Definition so the GUI's Save action can
// persist the result of compile→test as a real (but DISABLED) agent.
//
// The conversion lives here (not in the gateway) so it is unit-testable
// without an HTTP server. Persistence wiring (loader.Upsert into an agent
// dir) lives in the gateway handler; ToAgentDefinition only does the
// pure Draft -> agent.Definition mapping and always sets Enabled=false.
package studio

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/soulacy/soulacy/pkg/agent"
)

// slugRE collapses any run of non-alphanumeric chars into a single dash so
// a human-readable name becomes a stable, filesystem-safe agent id.
var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// ToAgentDefinition converts a Studio Draft into a disabled agent.Definition.
// It carries the workflow's name, trigger, channels, and graph into the
// agent's fields, mapping Draft.Flow onto the agent's WorkflowSpec graph
// form (nodes/edges/entry, per pkg/agent/workflow.go). Enabled is always
// false — Studio saves are staged, not live.
func ToAgentDefinition(draft Draft) (agent.Definition, error) {
	id := slug(draft.Name)
	if id == "" {
		return agent.Definition{}, fmt.Errorf("studio: cannot derive an agent id from an empty workflow name")
	}

	def := agent.Definition{
		ID:           id,
		Name:         draft.Name,
		Description:  "Created in Studio.",
		Trigger:      mapTrigger(draft.Trigger.Type),
		Channels:     append([]string(nil), draft.Channels...),
		SystemPrompt: "Studio-authored workflow agent.",
		// Disabled by construction: a Studio save stages an agent for the
		// operator to review and enable.
		Enabled: false,
		Workflow: &agent.WorkflowSpec{
			Nodes:             draft.Flow.Nodes,
			Edges:             draft.Flow.Edges,
			Entry:             draft.Flow.Entry,
			MaxNodeExecutions: 0,
		},
	}

	// Schedule triggers carry their cron into the agent Schedule block so
	// the scheduler can register the (disabled) agent unchanged.
	if def.Trigger == agent.TriggerCron {
		if cron, ok := draft.Trigger.Config["cron"].(string); ok && strings.TrimSpace(cron) != "" {
			def.Schedule = &agent.Schedule{Cron: cron}
		}
	}

	return def, nil
}

// mapTrigger translates Studio's trigger.type vocabulary (schedule | channel
// | webhook | manual) onto the agent's TriggerKind. "manual" has no direct
// agent equivalent; it maps to TriggerInternal (programmatic activation).
func mapTrigger(t string) agent.TriggerKind {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "schedule", "cron":
		return agent.TriggerCron
	case "channel":
		return agent.TriggerChannel
	case "webhook":
		return agent.TriggerWebhook
	case "manual", "internal":
		return agent.TriggerInternal
	default:
		return agent.TriggerChannel
	}
}

// slug derives a stable, lowercase, dash-separated agent id from a name.
func slug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
