package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Story E26: skill-source review + management endpoints.

func seedRegistriesConfig(t *testing.T) (string, *Server) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	seed := `
server:
  port: 18789
registries:
  - id: main
    type: http
    base_url: https://registry.example.com
    priority: 10
    auth_headers:
      Authorization: "Bearer sk_secret"
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, newTestGatewayWithCfgPath(t, "secret", cfgPath)
}

func TestListRegistries_RedactsAuthHeaders(t *testing.T) {
	_, s := seedRegistriesConfig(t)
	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/registries", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d %v", status, body)
	}
	regs := body["registries"].([]any)
	if len(regs) != 1 {
		t.Fatalf("registries = %v", regs)
	}
	r := regs[0].(map[string]any)
	if r["id"] != "main" || r["type"] != "http" || r["has_auth"] != true {
		t.Errorf("view = %v", r)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "sk_secret") {
		t.Error("auth header value leaked into the listing")
	}
}

func TestProbeRegistry_EndToEnd(t *testing.T) {
	// A fake skills.sh-style directory the gateway probes server-side.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"acme/skills/web-audit","slug":"web-audit","name":"Web Audit","source":"acme/skills"}]}`))
	})
	dir := httptest.NewServer(mux)
	defer dir.Close()

	_, s := seedRegistriesConfig(t)
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/registries/probe", "secret",
		`{"url":"`+dir.URL+`"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d %v", status, body)
	}
	if body["kind"] != "skillssh" {
		t.Errorf("kind = %v", body["kind"])
	}
	sug := body["suggested"].(map[string]any)
	if sug["Type"] != "skillssh" && sug["type"] != "skillssh" {
		t.Errorf("suggested = %v", sug)
	}

	// Garbage input → 400.
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/registries/probe", "secret", `{"url":"ftp://nope"}`)
	if status != http.StatusBadRequest {
		t.Errorf("ftp probe status = %d, want 400", status)
	}
}

func TestAddRegistry_AppendsAndValidates(t *testing.T) {
	cfgPath, s := seedRegistriesConfig(t)

	// Unknown type refused.
	status, _ := gatewayJSON(t, s, http.MethodPost, "/api/v1/registries", "secret",
		`{"id":"x","type":"warp","base_url":"https://x.example"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown type status = %d", status)
	}

	// Missing base_url for non-git refused.
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/registries", "secret",
		`{"id":"x","type":"skillssh"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("missing base_url status = %d", status)
	}

	// Duplicate id refused.
	status, _ = gatewayJSON(t, s, http.MethodPost, "/api/v1/registries", "secret",
		`{"id":"main","type":"http","base_url":"https://x.example"}`)
	if status != http.StatusConflict {
		t.Fatalf("dup id status = %d", status)
	}

	// Valid add persists, preserving the existing entry + auth headers.
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/registries", "secret",
		`{"id":"skills.sh","type":"skillssh","base_url":"https://skills.sh/","priority":50}`)
	if status != http.StatusOK {
		t.Fatalf("add status = %d %v", status, body)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := yaml.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	regs := disk["registries"].([]any)
	if len(regs) != 2 {
		t.Fatalf("disk registries = %v", regs)
	}
	first := regs[0].(map[string]any)
	if ah, _ := first["auth_headers"].(map[string]any); ah == nil || ah["Authorization"] != "Bearer sk_secret" {
		t.Errorf("existing auth headers lost: %v", first)
	}
	added := regs[1].(map[string]any)
	if added["type"] != "skillssh" || added["base_url"] != "https://skills.sh" {
		t.Errorf("added = %v (trailing slash should be trimmed)", added)
	}
	// Server section untouched.
	if srv, _ := disk["server"].(map[string]any); srv == nil || srv["port"] != 18789 {
		t.Errorf("server block mutated: %v", disk["server"])
	}
}

// Regression for the whatsapp_web pair handler on FRESH configs:
// fmt.Sprint(nil) == "<nil>" defeated every empty-check, so defaults were
// skipped and EnsureSidecarScript got an empty dir ("mkdir : no such file
// or directory" in the GUI). cfgMapStr treats missing keys as empty.
func TestCfgMapStr_NilSafety(t *testing.T) {
	m := map[string]any{"set": " value ", "wrongtype": 42}
	if got := cfgMapStr(m, "missing"); got != "" {
		t.Errorf("missing key = %q, want empty", got)
	}
	if got := cfgMapStr(m, "wrongtype"); got != "" {
		t.Errorf("non-string = %q, want empty", got)
	}
	if got := cfgMapStr(m, "set"); got != "value" {
		t.Errorf("set = %q", got)
	}
}
