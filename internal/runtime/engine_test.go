// engine_test.go — regression tests for the engine's most-bitten code paths.
// Avoids spinning up a real LLM (no network in CI) by exercising internal
// helpers directly. The full agent loop is covered by manual smoke tests
// in docs/REGRESSION_TESTING.md.
package runtime

import (
	"context"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// TestGetOrCreateSession_IsolatesByAgent guards against the session-bleed
// regression: before the fix, sessions were keyed by sessionID alone, so two
// different agents using the same fixed http session id ("http-gui-user")
// shared the same in-memory History. The fix keys by (agentID, sessionID).
func TestGetOrCreateSession_IsolatesByAgent(t *testing.T) {
	e := &Engine{}

	a := e.getOrCreateSession("shared-session-id", "agent-a")
	b := e.getOrCreateSession("shared-session-id", "agent-b")

	if a == b {
		t.Fatal("two agents with same session id MUST get distinct Session structs")
	}
	if a.AgentID != "agent-a" || b.AgentID != "agent-b" {
		t.Errorf("agent ids on sessions wrong: a=%q b=%q", a.AgentID, b.AgentID)
	}

	// Same agent + same session id should return the same struct.
	again := e.getOrCreateSession("shared-session-id", "agent-a")
	if again != a {
		t.Error("same (agent, session) lookup should return the cached session")
	}
}

// TestAgentCallDepth_Roundtrip verifies the context.Value-based depth counter
// used by runAgentCall to bound recursion.
func TestAgentCallDepth_Roundtrip(t *testing.T) {
	ctx := context.Background()
	if got := agentCallDepth(ctx); got != 0 {
		t.Errorf("fresh context: got depth %d, want 0", got)
	}
	ctx = withAgentCallDepth(ctx, 3)
	if got := agentCallDepth(ctx); got != 3 {
		t.Errorf("after withAgentCallDepth(3): got %d, want 3", got)
	}
	// Nesting overrides the inner value.
	inner := withAgentCallDepth(ctx, 5)
	if got := agentCallDepth(inner); got != 5 {
		t.Errorf("nested: got %d, want 5", got)
	}
	// Outer context unchanged.
	if got := agentCallDepth(ctx); got != 3 {
		t.Errorf("outer after inner overwrite: got %d, want 3", got)
	}
}

// TestResolveAgentRefs covers wildcard expansion, self-exclusion, and the
// disabled-peer filter — all of which protect against accidental loops and
// silently-broken delegations.
func TestResolveAgentRefs(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	// Pre-load three agents directly into the loader registry.
	defs := []*agent.Definition{
		{ID: "alpha", Enabled: true},
		{ID: "beta", Enabled: true},
		{ID: "disabled-one", Enabled: false},
	}
	for _, d := range defs {
		if err := l.Upsert(dir, d); err != nil {
			t.Fatalf("Upsert %s: %v", d.ID, err)
		}
	}

	e := &Engine{loader: l}

	// Wildcard: should return alpha and beta (disabled excluded, self excluded
	// when caller matches).
	got := e.resolveAgentRefs([]string{"*"}, "alpha")
	if len(got) != 1 || got[0].ID != "beta" {
		t.Errorf("wildcard from alpha: got %v, want [beta]", agentIDs(got))
	}

	// "all" is a synonym for "*".
	got = e.resolveAgentRefs([]string{"all"}, "beta")
	if len(got) != 1 || got[0].ID != "alpha" {
		t.Errorf("'all' from beta: got %v, want [alpha]", agentIDs(got))
	}

	// Explicit self-reference is dropped (no one-step infinite loop).
	got = e.resolveAgentRefs([]string{"alpha", "beta"}, "alpha")
	if len(got) != 1 || got[0].ID != "beta" {
		t.Errorf("self-ref drop: got %v, want [beta]", agentIDs(got))
	}

	// Unknown ids silently dropped.
	got = e.resolveAgentRefs([]string{"alpha", "ghost"}, "beta")
	if len(got) != 1 || got[0].ID != "alpha" {
		t.Errorf("unknown id drop: got %v, want [alpha]", agentIDs(got))
	}

	// Empty list returns nil.
	if got := e.resolveAgentRefs(nil, "x"); got != nil {
		t.Errorf("nil refs: got %v, want nil", agentIDs(got))
	}
}

func agentIDs(defs []*agent.Definition) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.ID
	}
	return out
}

// TestBuildAgentCallSchemas verifies that the agent__<id> tool schemas are
// generated correctly and that peers with empty descriptions get a sane
// fallback (the model picks tools by description, so an empty description
// would make the tool effectively invisible to the LLM).
func TestBuildAgentCallSchemas(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	for _, d := range []*agent.Definition{
		{ID: "researcher", Description: "Searches the KB.", Enabled: true},
		{ID: "critic", Description: "", Enabled: true}, // empty description
	} {
		if err := l.Upsert(dir, d); err != nil {
			t.Fatalf("Upsert %s: %v", d.ID, err)
		}
	}

	e := &Engine{loader: l}
	caller := &agent.Definition{ID: "writer", Agents: []string{"researcher", "critic"}}

	schemas := e.buildAgentCallSchemas(caller)
	if len(schemas) != 2 {
		t.Fatalf("schema count: got %d, want 2", len(schemas))
	}
	for _, s := range schemas {
		if s.Description == "" {
			t.Errorf("schema %q has empty description", s.Name)
		}
		if len(s.Name) == 0 || s.Name[:len(AgentToolPrefix)] != AgentToolPrefix {
			t.Errorf("schema name should start with %q, got %q", AgentToolPrefix, s.Name)
		}
		params, _ := s.Parameters["properties"].(map[string]any)
		if _, ok := params["message"]; !ok {
			t.Errorf("schema %q missing required `message` parameter", s.Name)
		}
	}
}

// TestProviderAllowed guards the llm.allowed_providers field added 2026-05-28
// after a cron agent intended for ollama accidentally got pointed at
// Anthropic via the GUI dropdown and produced a "credit balance too low"
// failure. The fix: agents that set `llm.allowed_providers: [ollama]` are
// hard-blocked from any other provider at engine entry.
//
// The matrix covers:
//  1. Empty/nil allowlist = legacy behaviour (every provider allowed).
//  2. Single-provider allowlist matches its own provider.
//  3. Single-provider allowlist rejects any other provider — the bug fix.
//  4. Multi-entry allowlist accepts members + rejects non-members.
//  5. Case sensitivity — names must match exactly (Ollama != ollama).
func TestProviderAllowed(t *testing.T) {
	cases := []struct {
		name      string
		allowlist []string
		provider  string
		want      bool
	}{
		{"nil allowlist permits any", nil, "anthropic", true},
		{"empty allowlist permits any", []string{}, "openai", true},
		{"single match", []string{"ollama"}, "ollama", true},
		{"single mismatch blocks", []string{"ollama"}, "anthropic", false},
		{"multi match", []string{"ollama", "openai"}, "openai", true},
		{"multi mismatch blocks", []string{"ollama", "openai"}, "anthropic", false},
		{"case-sensitive Ollama != ollama", []string{"Ollama"}, "ollama", false},
		{"empty provider with non-empty list blocks", []string{"ollama"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerAllowed(tc.allowlist, tc.provider)
			if got != tc.want {
				t.Errorf("providerAllowed(%v, %q) = %v, want %v",
					tc.allowlist, tc.provider, got, tc.want)
			}
		})
	}
}

func TestMCPToolAllowed(t *testing.T) {
	serversRocket := []string{"rocketmoney"}
	serversNone := []string{}
	serversWildcard := []string{"*"}
	toolsTxn := []string{"mcp__rocketmoney__get_transactions"}
	toolsNone := []string{}

	cases := []struct {
		name     string
		def      *agent.Definition
		fullName string
		want     bool
	}{
		{
			name:     "legacy absent allowlists permit all MCP tools",
			def:      &agent.Definition{ID: "legacy"},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     true,
		},
		{
			name:     "server allowlist permits every tool on that server",
			def:      &agent.Definition{ID: "finance", MCPServers: &serversRocket},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     true,
		},
		{
			name:     "server allowlist blocks other servers",
			def:      &agent.Definition{ID: "finance", MCPServers: &serversRocket},
			fullName: "mcp__filesystem__read_file",
			want:     false,
		},
		{
			name:     "explicit empty server allowlist disables MCP",
			def:      &agent.Definition{ID: "none", MCPServers: &serversNone},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     false,
		},
		{
			name:     "full tool allowlist permits one tool",
			def:      &agent.Definition{ID: "finance", MCPTools: &toolsTxn},
			fullName: "mcp__rocketmoney__get_transactions",
			want:     true,
		},
		{
			name:     "full tool allowlist blocks sibling tool",
			def:      &agent.Definition{ID: "finance", MCPTools: &toolsTxn},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     false,
		},
		{
			name:     "explicit empty tool allowlist disables MCP",
			def:      &agent.Definition{ID: "none", MCPTools: &toolsNone},
			fullName: "mcp__rocketmoney__get_accounts",
			want:     false,
		},
		{
			name:     "wildcard server allowlist permits all MCP",
			def:      &agent.Definition{ID: "all", MCPServers: &serversWildcard},
			fullName: "mcp__filesystem__read_file",
			want:     true,
		},
		{
			name:     "unsanitized server allowlist matches sanitized tool prefix",
			def:      &agent.Definition{ID: "mixed", MCPServers: &[]string{"Rocket Money"}},
			fullName: "mcp__rocket_money__get_transactions",
			want:     true,
		},
		{
			name:     "malformed MCP name is rejected once filtering is active",
			def:      &agent.Definition{ID: "bad", MCPServers: &serversRocket},
			fullName: "mcp__rocketmoney",
			want:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpToolAllowed(tc.def, tc.fullName); got != tc.want {
				t.Errorf("mcpToolAllowed(%+v, %q) = %v, want %v",
					tc.def, tc.fullName, got, tc.want)
			}
		})
	}
}
