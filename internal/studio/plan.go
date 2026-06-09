// plan.go — Studio's "plan" step (Story M2). Before a Studio draft is saved
// and exposed to channels, Plan classifies the agent it would become and
// decides whether saving would create a PRIVILEGED-tier channel exposure
// that needs the operator's explicit consent.
//
// The logic lives here (not in the gateway) so it is unit-testable without
// an HTTP server: POST /api/v1/studio/plan is a thin wrapper over Plan, and
// POST /api/v1/studio/save reuses the same decision to enforce consent.
package studio

import (
	"github.com/soulacy/soulacy/internal/tier"
)

// ConsentItem describes one concrete reason consent is being requested. For
// M2 the only kind is "channel": binding a Privileged-tier workflow to a
// non-web channel exposes the workflow's shell/write/install surface to that
// channel's users.
type ConsentItem struct {
	Kind   string `json:"kind"`   // "channel"
	Name   string `json:"name"`   // the channel being bound
	Reason string `json:"reason"` // why the workflow is privileged (from tier.Explain)
}

// PlanResult is the POST /api/v1/studio/plan response and the shared
// decision the save path enforces. Tier is the lowercase tier token
// ("read_only" is normalised to "readonly" for the wire contract).
type PlanResult struct {
	Tier            string        `json:"tier"`
	Reasons         []string      `json:"reasons"`
	RequiresConsent bool          `json:"requiresConsent"`
	ConsentItems    []ConsentItem `json:"consentItems"`
}

// Plan converts a draft into the agent.Definition it would be saved as,
// classifies its capability tier, and decides whether saving needs consent.
//
// requiresConsent is true iff BOTH:
//   - the workflow classifies as Privileged tier, AND
//   - the draft binds at least one channel.
//
// A Privileged workflow with no channel can't expose anything to channel
// users, so it needs no consent; an Active/ReadOnly workflow is allowed on
// channels by default (mirroring internal/app/channels.go's bindingDecision).
//
// When requiresConsent is true, one ConsentItem is emitted per bound channel,
// each carrying the tier reasons that justified the Privileged classification.
//
// The conversion is consent-agnostic (acceptPrivilegedExposure=false): Plan
// reports the situation, it does not apply consent.
func Plan(draft Draft) (PlanResult, error) {
	def, err := ToAgentDefinition(draft, false)
	if err != nil {
		return PlanResult{}, err
	}

	// No lookup: Studio classifies the draft in isolation. Named peer agents
	// referenced by the flow can't be resolved here, but the draft's own tool
	// surface (projected onto def.Builtins in ToAgentDefinition) is what
	// drives the Privileged decision for a freshly-authored workflow.
	exp := tier.Explain(&def, nil)

	res := PlanResult{
		Tier:    tierLabel(exp.Tier),
		Reasons: exp.Reasons,
	}
	if res.Reasons == nil {
		res.Reasons = []string{}
	}
	res.ConsentItems = []ConsentItem{}

	if exp.Tier == tier.Privileged && len(def.Channels) > 0 {
		res.RequiresConsent = true
		reason := joinReasons(exp.Reasons)
		for _, ch := range def.Channels {
			res.ConsentItems = append(res.ConsentItems, ConsentItem{
				Kind:   "channel",
				Name:   ch,
				Reason: reason,
			})
		}
	}

	return res, nil
}

// tierLabel maps a tier.Tier onto the wire vocabulary the frontend contract
// pins: readonly | active | privileged (note: tier.Tier.String() returns
// "read_only", which we collapse to "readonly" for the API).
func tierLabel(t tier.Tier) string {
	switch t {
	case tier.ReadOnly:
		return "readonly"
	case tier.Active:
		return "active"
	case tier.Privileged:
		return "privileged"
	default:
		return "unknown"
	}
}

// joinReasons renders the tier reasons into a single human-readable string
// for a ConsentItem. An empty reason set (shouldn't happen for Privileged,
// but stay defensive) yields a generic fallback.
func joinReasons(reasons []string) string {
	switch len(reasons) {
	case 0:
		return "workflow has privileged capabilities"
	case 1:
		return reasons[0]
	default:
		out := reasons[0]
		for _, r := range reasons[1:] {
			out += "; " + r
		}
		return out
	}
}
