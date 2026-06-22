package studio

import "strings"

// ModelPreset holds runtime defaults tuned for a model class (Stories #23/#24).
// Local models are slower, so their agents need more generous timeouts than the
// engine's cloud-oriented defaults — otherwise a perfectly good run is killed
// mid-thought. Empty string fields mean "leave the engine default".
type ModelPreset struct {
	MaxTurns     int    `json:"max_turns"`
	RunTimeout   string `json:"run_timeout"`   // whole-run wall clock
	StepTimeout  string `json:"step_timeout"`  // per reasoning step
	TotalTimeout string `json:"total_timeout"` // whole reasoning task
}

// LocalPresetFor returns timeout/turn defaults appropriate for running an agent
// on the given LOCAL model. Pure + deterministic. Smaller models get the most
// generous timeouts (they're slowest token-for-token); capable local models get
// a moderate bump over cloud defaults. Returns the zero ModelPreset for a model
// we don't want to special-case (caller then keeps engine defaults).
func LocalPresetFor(model string) ModelPreset {
	if isSmallModel(model) && !isStrongModel(model) {
		// Small local model: be patient.
		return ModelPreset{MaxTurns: 10, RunTimeout: "900s", StepTimeout: "180s", TotalTimeout: "900s"}
	}
	// Any other local model: a solid, generous-but-bounded default.
	return ModelPreset{MaxTurns: 15, RunTimeout: "600s", StepTimeout: "120s", TotalTimeout: "600s"}
}

// LocalPresetName is a short, human label for the preset a model maps to, for
// display in the UI.
func LocalPresetName(model string) string {
	if strings.TrimSpace(model) == "" {
		return "local default"
	}
	if isSmallModel(model) && !isStrongModel(model) {
		return "compact local (patient timeouts)"
	}
	return "local default"
}
