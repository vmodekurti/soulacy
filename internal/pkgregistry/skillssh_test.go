package pkgregistry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
)

// fakeSkillsSh serves the subset of the skills.sh API the provider uses.
func fakeSkillsSh(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			http.Error(w, `{"error":"bad_request"}`, 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{
				"id": "acme/skills/web-audit", "slug": "web-audit",
				"name": "Web Audit", "source": "acme/skills",
				"installs": 1234, "sourceType": "github",
				"installUrl": "https://github.com/acme/skills",
				"url":        "https://skills.example/acme/skills/web-audit",
			}},
		})
	})
	mux.HandleFunc("/api/v1/skills/audit/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"audits": []map[string]any{
				{"provider": "Gen Agent Trust Hub", "status": "pass", "riskLevel": "LOW"},
				{"provider": "Socket", "status": "warn", "summary": "1 alert"},
			},
		})
	})
	mux.HandleFunc("/api/v1/skills/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/skills/")
		if id != "acme/skills/web-audit" {
			http.Error(w, `{"error":"not_found"}`, 404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "source": "acme/skills", "slug": "web-audit",
			"installs": 1234, "hash": "abcdef1234567890",
			"files": []map[string]string{
				{"path": "SKILL.md", "contents": "---\nname: Web Audit\n---\nAudit a site."},
				{"path": "scripts/run.py", "contents": "print('hi')"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestSkillsShProvider(t *testing.T, base string) sdkpkg.Provider {
	t.Helper()
	p, err := newSkillsShProvider(map[string]any{"id": "skills-sh", "base_url": base})
	if err != nil {
		t.Fatalf("newSkillsShProvider: %v", err)
	}
	return p
}

func TestSkillsSh_Search(t *testing.T) {
	srv := fakeSkillsSh(t)
	p := newTestSkillsShProvider(t, srv.URL)
	pkgs, err := p.Search(context.Background(), "audit")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Slug != "acme/skills/web-audit" {
		t.Fatalf("pkgs = %+v", pkgs)
	}
	if !strings.Contains(pkgs[0].Description, "Web Audit") {
		t.Errorf("description = %q", pkgs[0].Description)
	}
}

func TestSkillsSh_ResolveKnownAndUnknown(t *testing.T) {
	srv := fakeSkillsSh(t)
	p := newTestSkillsShProvider(t, srv.URL)

	pkg, err := p.Resolve(context.Background(), "acme/skills/web-audit")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pkg.Slug != "acme/skills/web-audit" || pkg.Version == "" {
		t.Errorf("pkg = %+v", pkg)
	}
	// Partner audits surface in the description for the consent prompt.
	if !strings.Contains(pkg.Description, "Gen Agent Trust Hub") || !strings.Contains(pkg.Description, "warn") {
		t.Errorf("audits missing from description: %q", pkg.Description)
	}

	_, err = p.Resolve(context.Background(), "nobody/nothing/ghost")
	if !errors.Is(err, sdkpkg.ErrNotFound) {
		t.Errorf("unknown slug: err = %v, want ErrNotFound", err)
	}
}

func TestSkillsSh_FetchWritesFiles(t *testing.T) {
	srv := fakeSkillsSh(t)
	p := newTestSkillsShProvider(t, srv.URL)
	pkg, err := p.Resolve(context.Background(), "acme/skills/web-audit")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dst := t.TempDir()
	if err := p.Fetch(context.Background(), pkg, dst); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "SKILL.md"))
	if err != nil || !strings.Contains(string(b), "Web Audit") {
		t.Errorf("SKILL.md: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dst, "scripts", "run.py")); err != nil {
		t.Errorf("nested file missing: %v", err)
	}
}

func TestSkillsSh_FetchRefusesTraversal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/skills/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "evil/skills/x", "hash": "h",
			"files": []map[string]string{
				{"path": "../escape.txt", "contents": "boom"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := newTestSkillsShProvider(t, srv.URL)
	err := p.Fetch(context.Background(), sdkpkg.Package{Slug: "evil/skills/x", Source: "evil/skills/x"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal refusal, got %v", err)
	}
}

func TestSkillsSh_RequiresBaseURLDefault(t *testing.T) {
	p, err := newSkillsShProvider(map[string]any{"id": "s"})
	if err != nil {
		t.Fatalf("default base_url should apply: %v", err)
	}
	if !strings.Contains(p.baseURL, "skills.sh") {
		t.Errorf("default base = %q", p.baseURL)
	}
}
