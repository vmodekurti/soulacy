package channels

import "testing"

func TestActivationPolicyRequiresTriggerPhrase(t *testing.T) {
	p := ActivationPolicy{TriggerPhrase: "!soulacy", IgnoreGroups: true}

	if _, ok := p.Apply("hello", "thread", "user", false); ok {
		t.Fatal("message without trigger phrase should be ignored")
	}
	text, ok := p.Apply("!soulacy hello", "thread", "user", false)
	if !ok {
		t.Fatal("message with trigger phrase should be accepted")
	}
	if text != "hello" {
		t.Fatalf("trigger phrase should be stripped, got %q", text)
	}
}

func TestActivationPolicyIgnoresGroups(t *testing.T) {
	p := ActivationPolicy{TriggerPhrase: "!soulacy", IgnoreGroups: true}

	if _, ok := p.Apply("!soulacy hello", "thread", "user", true); ok {
		t.Fatal("group message should be ignored when IgnoreGroups is enabled")
	}
}

func TestActivationPolicyAllowLists(t *testing.T) {
	p := ActivationPolicy{
		TriggerPhrase:    "!soulacy",
		AllowedThreadIDs: []string{"allowed-thread"},
		AllowedUserIDs:   []string{"allowed-user"},
	}

	if _, ok := p.Apply("!soulacy hello", "other-thread", "allowed-user", false); ok {
		t.Fatal("message from disallowed thread should be ignored")
	}
	if _, ok := p.Apply("!soulacy hello", "allowed-thread", "other-user", false); ok {
		t.Fatal("message from disallowed user should be ignored")
	}
	if _, ok := p.Apply("!soulacy hello", "allowed-thread", "allowed-user", false); !ok {
		t.Fatal("message matching thread and user allowlists should be accepted")
	}
}
