package discord

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryFactory(t *testing.T) {
	a, ok, err := registry.NewChannel("discord", map[string]any{
		"id": "discord-x", "token": "Bot abc", "agent_id": "a", "guild_id": "g",
	})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if a.ID() != "discord-x" {
		t.Fatalf("ID = %q", a.ID())
	}
	if _, _, err := registry.NewChannel("discord", map[string]any{}); err == nil {
		t.Fatal("missing token must error")
	}
}
