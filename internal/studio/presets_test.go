package studio

import "testing"

func TestLocalPresetFor(t *testing.T) {
	small := LocalPresetFor("llama3:8b")
	big := LocalPresetFor("llama3:70b")
	if small.RunTimeout != "900s" {
		t.Errorf("small local model should get the most patient timeout, got %q", small.RunTimeout)
	}
	if big.RunTimeout != "600s" {
		t.Errorf("capable local model should get standard local timeout, got %q", big.RunTimeout)
	}
	if small.StepTimeout == "" || big.StepTimeout == "" {
		t.Error("presets should set a step timeout")
	}
}
