package main

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

// Story E10: every built-in driver must be registry-routed in the shipped
// binary. These assertions pin the contract — if a driver's init()
// self-registration or the generated blank-import file goes missing, this
// fails before any user notices a silently absent channel.
func TestAllBuiltinsRegistered(t *testing.T) {
	want := map[string][]string{
		"channels":  {"discord", "slack", "telegram", "whatsapp"},
		"providers": {"anthropic", "gemini", "google", "ollama", "openai"},
		"queues":    {"memory", "nats"},
		"vectors":   {"qdrant", "sqlite-vec"},
		"reasoning": {"plan_execute", "react"},
	}
	got := map[string][]string{
		"channels":  registry.Channels(),
		"providers": registry.Providers(),
		"queues":    registry.Queues(),
		"vectors":   registry.Vectors(),
		"reasoning": registry.ReasoningStrategies(),
	}
	for kind, names := range want {
		have := map[string]bool{}
		for _, n := range got[kind] {
			have[n] = true
		}
		for _, n := range names {
			if !have[n] {
				t.Errorf("%s registry missing built-in %q (have %v)", kind, n, got[kind])
			}
		}
	}
}
