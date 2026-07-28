package studio

import "strings"

// StrategyAdvice is Studio's server-side execution decision. The LLM may
// refine the user's prompt, but Soulacy owns this routing decision so graph
// architecture stays predictable.
type StrategyAdvice struct {
	Mode                 string `json:"mode"` // workflow | auto | plan_execute | react
	RuntimeStrategy      string `json:"runtime_strategy,omitempty"`
	Reason               string `json:"reason"`
	Confidence           string `json:"confidence,omitempty"`
	Provider             string `json:"provider,omitempty"`
	Model                string `json:"model,omitempty"`
	Local                bool   `json:"local,omitempty"`
	Strong               bool   `json:"strong,omitempty"`
	Compact              bool   `json:"compact,omitempty"`
	DeterministicPattern string `json:"deterministic_pattern,omitempty"`
	// CapabilityWarning is set when the chosen mode exceeds what the selected
	// model can actually do (P0-5). Advisory, never blocking: the operator may
	// know something the registry doesn't, but they should not find out from a
	// 3am failure that their model can't emit well-formed tool arguments.
	CapabilityWarning string `json:"capability_warning,omitempty"`
	// Capabilities is the resolved profile the advice was based on, so the UI
	// can show WHY a mode was recommended instead of asserting it.
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// AdviseStrategy is the single rule-based Strategy Advisor for Studio. It is
// deliberately deterministic: model capability can bias Auto vs Plan-Execute,
// but it never lets an LLM choose the architecture or implicitly choose ReAct.
func AdviseStrategy(intent string, cat Catalog, requested string, forceWorkflow bool) StrategyAdvice {
	intent = strings.TrimSpace(intent)
	req := normalizeMode(requested)
	advice := StrategyAdvice{
		Mode:       "auto",
		Confidence: "medium",
		Reason:     "Defaulting to Auto for an interactive tool-capable agent.",
	}
	if cat.Generation != nil {
		advice.Provider = cat.Generation.Provider
		advice.Model = cat.Generation.Model
		advice.Local = cat.Generation.Local
		advice.Strong = cat.Generation.Strong
		advice.Compact = cat.Generation.Compact
	}

	// Resolve the model's measured capabilities once; every branch below may
	// consult them (P0-5). Replaces the name-substring heuristics that scored an
	// unrecognised model "assumed fine".
	caps := LookupCapabilities(advice.Provider, advice.Model)
	advice.Capabilities = &caps
	// Only gate on capabilities when a model has actually been CHOSEN. "No
	// model selected yet" (a bare intent classification) is a different state
	// from "a named model we don't recognise", and conflating them would let
	// the gate override intent-based advice before there is anything to judge.
	modelChosen := strings.TrimSpace(advice.Model) != ""

	if explicitReActRequested(intent) {
		advice.Mode = "react"
		advice.RuntimeStrategy = "react"
		advice.Confidence = "medium"
		advice.Reason = "The user explicitly requested a ReAct-style loop; Studio will not choose ReAct implicitly."
		// Honour the explicit request, but say plainly when the model cannot
		// sustain it — this is the case where silence costs the most.
		if w := StrategyWarning(advice.Provider, advice.Model, "react"); modelChosen && w != "" {
			advice.CapabilityWarning = w
			advice.Confidence = "low"
		}
		return advice
	}
	pattern := deterministicWorkflowPattern(intent)
	// An interactive intent must not be captured by a pipeline pattern.
	//
	// The digest patterns match on topic keywords, so "the agent SEARCHES for
	// travel DEALS when a user asks" satisfied deal_digest and Studio built a
	// fixed two-node graph with HIGH confidence — for a prompt that said, in as
	// many words, that the agent responds on demand and asks clarifying questions
	// first. A fixed graph cannot ask a question and branch on the reply.
	//
	// An EXPLICIT request for a workflow still wins: the user is allowed to want
	// a pipeline. This only stops keyword matching from making that choice for
	// them.
	explicitlyWorkflow := forceWorkflow || req == "workflow" || explicitWorkflowRequested(intent)
	if pattern != "" && !explicitlyWorkflow && ConversationalIntent(intent) {
		pattern = ""
	}
	if explicitlyWorkflow || pattern != "" {
		advice.Mode = "workflow"
		advice.RuntimeStrategy = ""
		advice.Confidence = "high"
		advice.DeterministicPattern = pattern
		if pattern == "" {
			pattern = "fixed workflow"
		}
		advice.Reason = "Soulacy selected Workflow because the intent describes a deterministic " + pattern + " pipeline."
		return advice
	}
	if req == "plan_execute" || hasPlanExecuteCues(intent) || dynamicSkillRoutingIntent(intent) {
		// Capability gate (P0-5): a model that cannot hold a plan together will
		// fail planning and silently degrade to ReAct. Steer to a fixed
		// workflow instead of setting up that failure, unless the operator
		// asked for Plan-Execute explicitly — in which case warn and comply.
		if planOK, why := caps.SupportsPlanExecute(); modelChosen && !planOK && req != "plan_execute" {
			advice.Mode = "workflow"
			advice.RuntimeStrategy = ""
			advice.Confidence = "high"
			advice.Reason = "Soulacy selected Workflow because the task suits planning but " +
				modelLabel(caps) + " cannot sustain it: " + why + "."
			return advice
		}
		advice.Mode = "plan_execute"
		advice.RuntimeStrategy = "plan_execute"
		advice.Confidence = "high"
		if req == "plan_execute" {
			advice.Reason = "The user selected Plan-Execute for a tool-agent with explicit planning."
			if w := StrategyWarning(advice.Provider, advice.Model, "plan_execute"); modelChosen && w != "" {
				advice.CapabilityWarning = w
				advice.Confidence = "low"
			}
		} else {
			advice.Reason = "Soulacy selected Plan-Execute because the task needs multi-phase or dynamic tool routing without a stable fixed workflow."
		}
		return advice
	}
	if req == "auto" {
		advice.Mode = "auto"
		advice.RuntimeStrategy = "auto"
		advice.Confidence = "high"
		advice.Reason = "The user selected Auto; the runtime can use native tool calling when the model supports it."
		return advice
	}

	li := strings.ToLower(intent)
	if hasStrongReasoningCues(intent) && !interactiveAssistantIntent(li) {
		advice.Mode = "plan_execute"
		advice.RuntimeStrategy = "plan_execute"
		advice.Confidence = "medium"
		advice.Reason = "Soulacy selected Plan-Execute because the task needs multi-phase reasoning without a stable fixed workflow."
		return advice
	}
	// Capability-driven (P0-5), replacing the model-name substring heuristic:
	// Auto leans on native tool calling and short adaptive plans, so require the
	// model to actually have them rather than inferring it from the name.
	if modelChosen && caps.Known && caps.NativeTools && caps.ArgAccuracy >= ReActMinArgAccuracy {
		advice.Mode = "auto"
		advice.RuntimeStrategy = "auto"
		advice.Confidence = "high"
		advice.Reason = "Soulacy selected Auto because " + modelLabel(caps) +
			" has native tool calling and reliable tool-argument accuracy."
		return advice
	}
	// An unprofiled model must not land in Auto by omission — that is the
	// optimistic default P0-5 exists to remove.
	if modelChosen && !caps.Known {
		advice.Mode = "workflow"
		advice.RuntimeStrategy = ""
		advice.Confidence = "medium"
		advice.Reason = "Soulacy selected Workflow because " + modelLabel(caps) +
			" has not been profiled, and a fixed graph asks the least of an unknown model."
		advice.CapabilityWarning = caps.Notes
		return advice
	}
	if cat.Generation != nil && cat.Generation.Compact && hasStrongReasoningCues(intent) {
		advice.Mode = "plan_execute"
		advice.RuntimeStrategy = "plan_execute"
		advice.Confidence = "medium"
		advice.Reason = "Soulacy selected Plan-Execute because compact/local models perform better with explicit planning scaffolds."
		return advice
	}
	advice.RuntimeStrategy = "auto"
	return advice
}

func deterministicWorkflowPattern(intent string) string {
	li := strings.ToLower(intent)
	switch {
	case deterministicNotebookPodcastWorkflow(intent):
		return "NotebookLM podcast"
	case knowledgeIngestionWorkflow(intent):
		return "knowledge ingestion"
	case researchDigestWorkflow(intent):
		return "research digest"
	case dealDigestWorkflow(intent):
		return "deal digest"
	case stockDigestWorkflow(intent):
		return "market digest"
	case scheduledDeliveryWorkflow(intent):
		return "scheduled delivery"
	case structuredWorkflowProcedureRequested(intent):
		return "numbered procedure"
	case anyContains(li, "fixed workflow", "deterministic workflow", "as a workflow"):
		return "fixed workflow"
	default:
		return ""
	}
}

func interactiveAssistantIntent(li string) bool {
	return anyContains(li,
		"chat", "answer", "interactive", "assistant", "ask", "question",
		"weather", "stock advisor", "options", "explain", "help me")
}

func dynamicSkillRoutingIntent(intent string) bool {
	li := strings.ToLower(intent)
	return anyContains(li,
		"appropriate skill", "appropriate skills", "best-matching skill",
		"right skill", "selects and calls", "select the skill",
		"selects the skill", "choose the skill", "which skill", "which tool",
		"based on the parsed intent", "based on the question",
		"depending on the question", "selects the appropriate",
		"select the appropriate", "routes to the", "route to the")
}
