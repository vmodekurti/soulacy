package telegram

import (
	"testing"

	"github.com/soulacy/soulacy/sdk/registry"
)

func TestRegistryFactoryBuildsAdapter(t *testing.T) {
	a, ok, err := registry.NewChannel("telegram", map[string]any{
		"id":       "telegram-second",
		"token":    "123:abc",
		"agent_id": "assistant",
	})
	if !ok {
		t.Fatal("telegram factory not registered")
	}
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if got := a.ID(); got != "telegram-second" {
		t.Fatalf("adapter ID = %q", got)
	}
}

func TestRegistryFactoryDefaultID(t *testing.T) {
	a, ok, err := registry.NewChannel("telegram", map[string]any{"token": "123:abc"})
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got := a.ID(); got != "telegram" {
		t.Fatalf("default adapter ID = %q", got)
	}
}

func TestRegistryFactoryRequiresToken(t *testing.T) {
	_, ok, err := registry.NewChannel("telegram", map[string]any{"agent_id": "x"})
	if !ok {
		t.Fatal("factory should be registered")
	}
	if err == nil {
		t.Fatal("missing token must error")
	}
}
