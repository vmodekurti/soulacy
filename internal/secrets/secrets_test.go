package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

// fakeVault is an in-memory credentials.Vault for tests.
type fakeVault struct {
	data map[string]map[string][]byte // agentID -> key -> value
}

func newFakeVault() *fakeVault { return &fakeVault{data: map[string]map[string][]byte{}} }

func (f *fakeVault) Set(_ context.Context, agentID, key string, value []byte) error {
	if f.data[agentID] == nil {
		f.data[agentID] = map[string][]byte{}
	}
	f.data[agentID][key] = append([]byte(nil), value...)
	return nil
}
func (f *fakeVault) Get(_ context.Context, agentID, key string) ([]byte, error) {
	if v, ok := f.data[agentID][key]; ok {
		return v, nil
	}
	return nil, os.ErrNotExist
}
func (f *fakeVault) Delete(_ context.Context, agentID, key string) error {
	delete(f.data[agentID], key)
	return nil
}
func (f *fakeVault) List(_ context.Context, agentID string) ([]string, error) {
	var keys []string
	for k := range f.data[agentID] {
		keys = append(keys, k)
	}
	return keys, nil
}
func (f *fakeVault) WriteBlob(ctx context.Context, a, k string, d []byte) error {
	return f.Set(ctx, a, k, d)
}
func (f *fakeVault) ReadBlob(ctx context.Context, a, k string) ([]byte, error) {
	return f.Get(ctx, a, k)
}
func (f *fakeVault) Close() error { return nil }

func TestSetGetListDelete(t *testing.T) {
	ctx := context.Background()
	m := New(newFakeVault())
	if !m.Enabled() {
		t.Fatal("manager should be enabled")
	}
	if err := m.Set(ctx, "ANTHROPIC_API_KEY", "sk-ant-xyz"); err != nil {
		t.Fatal(err)
	}
	if v, ok := m.Get(ctx, "ANTHROPIC_API_KEY"); !ok || v != "sk-ant-xyz" {
		t.Fatalf("Get = %q,%v", v, ok)
	}
	names, _ := m.List(ctx)
	if len(names) != 1 || names[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("List = %v", names)
	}
	if err := m.Delete(ctx, "ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get(ctx, "ANTHROPIC_API_KEY"); ok {
		t.Fatal("expected deleted")
	}
}

func TestNilVaultSafe(t *testing.T) {
	ctx := context.Background()
	m := New(nil)
	if m.Enabled() {
		t.Fatal("nil vault must not be enabled")
	}
	if _, ok := m.Get(ctx, "x"); ok {
		t.Fatal("Get on nil vault should miss")
	}
	if err := m.Set(ctx, "x", "y"); err != ErrNoVault {
		t.Fatalf("Set on nil vault = %v", err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	ctx := context.Background()
	m := New(newFakeVault())
	_ = m.Set(ctx, "llm.providers.anthropic.api_key", "from-vault")
	t.Setenv("ANTHROPIC_API_KEY", "from-env")

	// vault wins
	if got := m.Resolve(ctx, "llm.providers.anthropic.api_key", "ANTHROPIC_API_KEY", "from-config"); got != "from-vault" {
		t.Fatalf("vault precedence: got %q", got)
	}
	// env wins over config when vault empty
	if got := m.Resolve(ctx, "missing", "ANTHROPIC_API_KEY", "from-config"); got != "from-env" {
		t.Fatalf("env precedence: got %q", got)
	}
	// config fallback
	if got := m.Resolve(ctx, "missing", "UNSET_ENV_VAR_XYZ", "from-config"); got != "from-config" {
		t.Fatalf("config fallback: got %q", got)
	}
}

func sampleConfig() *config.Config {
	c := &config.Config{}
	c.LLM.Providers = map[string]config.ProviderConfig{
		"anthropic": {APIKey: "sk-ant-plain"},
		"ollama":    {APIKey: ""},
	}
	c.Channels = map[string]map[string]any{
		"slack": {"bot_token": "xoxb-plain", "app_token": "xapp-plain", "agent_id": "a"},
	}
	c.Server.APIKey = "gateway-plain"
	return c
}

func TestOverlay(t *testing.T) {
	ctx := context.Background()
	m := New(newFakeVault())
	_ = m.Set(ctx, "llm.providers.anthropic.api_key", "vault-anthropic")
	_ = m.Set(ctx, "channels.slack.bot_token", "vault-bot")
	_ = m.Set(ctx, "server.api_key", "vault-gw")

	cfg := sampleConfig()
	n := m.Overlay(ctx, cfg)
	if n != 2 {
		t.Fatalf("overlaid %d, want 2 (server.api_key is not vault-managed)", n)
	}
	if cfg.LLM.Providers["anthropic"].APIKey != "vault-anthropic" {
		t.Error("anthropic not overlaid")
	}
	if cfg.Channels["slack"]["bot_token"] != "vault-bot" {
		t.Error("slack bot_token not overlaid")
	}
	// server.api_key must NOT be overlaid — it stays the config bootstrap key.
	if cfg.Server.APIKey != "gateway-plain" {
		t.Errorf("server key should be untouched, got %q", cfg.Server.APIKey)
	}
}

func TestCatalog(t *testing.T) {
	ctx := context.Background()
	m := New(newFakeVault())
	_ = m.Set(ctx, "llm.providers.anthropic.api_key", "v")
	_ = m.Set(ctx, "ALPHAVANTAGE_API_KEY", "custom") // custom/tool secret

	cat := m.Catalog(ctx, sampleConfig())
	byName := map[string]Descriptor{}
	for _, d := range cat {
		byName[d.Name] = d
	}
	if d, ok := byName["llm.providers.anthropic.api_key"]; !ok || !d.Set || d.Category != CategoryLLM {
		t.Errorf("anthropic descriptor wrong: %+v ok=%v", d, ok)
	}
	if d, ok := byName["channels.slack.bot_token"]; !ok || d.Set || d.Category != CategoryChannel {
		t.Errorf("slack descriptor wrong: %+v ok=%v", d, ok)
	}
	if d, ok := byName["ALPHAVANTAGE_API_KEY"]; !ok || d.Category != CategoryTool || !d.Set {
		t.Errorf("custom descriptor wrong: %+v ok=%v", d, ok)
	}
}

func TestMigrate(t *testing.T) {
	ctx := context.Background()
	m := New(newFakeVault())

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := `server:
  host: "127.0.0.1"
  api_key: "gateway-plain"

llm:
  providers:
    anthropic:
      api_key: "sk-ant-plain"   # rotate me

channels:
  slack:
    bot_token: "xoxb-plain"
    app_token: ""
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := sampleConfig()
	n, err := m.Migrate(ctx, cfg, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected migrations")
	}
	// Migrated provider/channel values now in vault.
	if v, ok := m.Get(ctx, "llm.providers.anthropic.api_key"); !ok || v != "sk-ant-plain" {
		t.Errorf("anthropic not in vault: %q", v)
	}
	// server.api_key must NOT be migrated — it stays the bootstrap key.
	if _, ok := m.Get(ctx, "server.api_key"); ok {
		t.Error("server.api_key should NOT be migrated into the vault")
	}
	// File: migrated secrets blanked; gateway key + comments + structure kept.
	out, _ := os.ReadFile(cfgPath)
	s := string(out)
	if strings.Contains(s, "sk-ant-plain") || strings.Contains(s, "xoxb-plain") {
		t.Errorf("migrated secret still in file:\n%s", s)
	}
	if !strings.Contains(s, `api_key: "gateway-plain"`) {
		t.Errorf("gateway api_key must remain in config:\n%s", s)
	}
	if !strings.Contains(s, "# rotate me") {
		t.Error("comment not preserved")
	}
	if !strings.Contains(s, `host: "127.0.0.1"`) {
		t.Error("non-secret value altered")
	}
}

func TestRedactSecretValues(t *testing.T) {
	in := `  api_key: "secret123"
  api_key: "gateway-keep"
  base_url: "http://x"  # keep
  bot_token: xoxb-abc # inline comment
  model: "llama3"
`
	moved := map[string]bool{"secret123": true, "xoxb-abc": true}
	out, n := RedactSecretValues(in, moved)
	if n != 2 {
		t.Fatalf("changed %d, want 2", n)
	}
	if strings.Contains(out, "secret123") || strings.Contains(out, "xoxb-abc") {
		t.Errorf("migrated secrets remain:\n%s", out)
	}
	if !strings.Contains(out, `api_key: "gateway-keep"`) {
		t.Errorf("unmigrated api_key must be kept:\n%s", out)
	}
	if !strings.Contains(out, "# inline comment") {
		t.Error("inline comment dropped")
	}
}
