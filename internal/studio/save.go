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

// StudioPrivilegeAckLabel is the agent-definition label key under which
// Studio records that the USER acknowledged, at save time, that this
// workflow is Privileged-tier and bound to a (non-web) channel.
//
// This is an informational acknowledgment record ONLY — it is deliberately
// NOT the operator's binding consent. By design (see internal/app/channels.go
// bindingDecision), the authoritative `accept_privileged_exposure` flag MUST
// live on the config.yaml channel binding, because the operator deploying an
// agent to a public channel is the one accepting the risk, not the agent
// author. Studio saves the agent DISABLED; the operator still grants channel
// exposure at deploy time. We use a distinct key so this record can never be
// mistaken for — or silently promoted into — the binding flag.
const StudioPrivilegeAckLabel = "studio.privilege_acknowledged"

// ToAgentDefinition converts a Studio Draft into a disabled agent.Definition.
// It carries the workflow's name, trigger, channels, and graph into the
// agent's fields, mapping Draft.Flow onto the agent's WorkflowSpec graph
// form (nodes/edges/entry, per pkg/agent/workflow.go). Enabled is always
// false — Studio saves are staged, not live.
//
// acceptPrivilegedExposure records the USER's save-time acknowledgment: when
// true and the draft binds at least one channel, it is stamped on the agent's
// Labels under StudioPrivilegeAckLabel as an informational record. It does NOT
// grant the channel binding — the operator must still set
// accept_privileged_exposure on the config.yaml binding at deploy time
// (internal/app/channels.go). The agent is saved disabled regardless.
func ToAgentDefinition(draft Draft, acceptPrivilegedExposure bool) (agent.Definition, error) {
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

	// Project the flow's capability surface onto the Definition so the tier
	// classifier (internal/tier) sees what the workflow can actually DO. A
	// flow tool node naming `shell_exec`/`write_file` must classify the
	// agent Privileged exactly as if it had been listed in `builtins:`; a
	// flow agent node naming a peer feeds transitive peer detection.
	if builtins := flowBuiltins(draft.Flow); len(builtins) > 0 {
		def.Builtins = &builtins
	}
	if peers := flowPeers(draft.Flow); len(peers) > 0 {
		def.Agents = peers
	}

	// Schedule triggers carry their cron into the agent Schedule block so
	// the scheduler can register the (disabled) agent unchanged.
	if def.Trigger == agent.TriggerCron {
		if cron, ok := draft.Trigger.Config["cron"].(string); ok && strings.TrimSpace(cron) != "" {
			def.Schedule = &agent.Schedule{Cron: cron}
		}
	}

	// Record the user's save-time acknowledgment on the Definition. Only
	// meaningful when a channel is actually bound (no binding → no privileged
	// exposure to acknowledge), so we gate on Channels to avoid a stray label.
	// This is informational only — it does not grant the channel binding.
	if acceptPrivilegedExposure && len(def.Channels) > 0 {
		if def.Labels == nil {
			def.Labels = map[string]string{}
		}
		def.Labels[StudioPrivilegeAckLabel] = "true"
	}

	return def, nil
}

// flowBuiltins collects the distinct, non-empty tool names from a flow's
// tool nodes, in first-seen order, so they can populate def.Builtins for
// tier classification. Agent and branch nodes are ignored.
func flowBuiltins(flow Flow) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range flow.Nodes {
		tool := strings.TrimSpace(n.Tool)
		if tool == "" || seen[tool] {
			continue
		}
		seen[tool] = true
		out = append(out, tool)
	}
	return out
}

// flowPeers collects the distinct, non-empty peer agent ids referenced by a
// flow's agent nodes, in first-seen order, so they populate def.Agents and
// feed the tier classifier's transitive peer walk.
func flowPeers(flow Flow) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range flow.Nodes {
		a := strings.TrimSpace(n.Agent)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
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
