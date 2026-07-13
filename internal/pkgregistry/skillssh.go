package pkgregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/cfgmap"
	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
)

// DefaultSkillsShBase is the public skills.sh directory.
const DefaultSkillsShBase = "https://skills.sh"

// skillsShProvider talks to a skills.sh-style directory (Story E26):
//
//	GET {base}/api/v1/skills/search?q={q}      → {"data":[Skill…]}
//	GET {base}/api/v1/skills/{source}/{slug}   → skill detail incl. full file tree
//	GET {base}/api/v1/skills/audit/{id}        → partner security audits
//
// Slugs are skills.sh ids: "{owner}/{repo}/{skill}" (GitHub sources) or
// "{domain}/{skill}" (well-known sources). Files arrive inline as JSON, so
// Fetch materialises them directly — no archive, no checksum (the detail
// response's hash is recorded as the version for change detection).
type skillsShProvider struct {
	id      string
	baseURL string
	headers map[string]string
	client  *http.Client
}

func newSkillsShProvider(cfg map[string]any) (*skillsShProvider, error) {
	base := strings.TrimRight(cfgmap.Str(cfg, "base_url", DefaultSkillsShBase), "/")
	headers := map[string]string{}
	for k, v := range cfgmap.Map(cfg, "auth_headers") {
		if s, ok := v.(string); ok {
			headers[k] = s
		}
	}
	if len(headers) == 0 && strings.Contains(base, "skills.sh") {
		if token := strings.TrimSpace(os.Getenv("VERCEL_OIDC_TOKEN")); token != "" {
			headers["Authorization"] = "Bearer " + token
		}
	}
	return &skillsShProvider{
		id:      cfgmap.Str(cfg, "id", "skillssh"),
		baseURL: base,
		headers: headers,
		client:  &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (p *skillsShProvider) ID() string { return p.id }

func (p *skillsShProvider) getJSON(ctx context.Context, rawURL string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, fmt.Errorf("pkgregistry: %w", err)
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("pkgregistry: %s: %w", p.id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(b))
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &body) == nil {
			switch {
			case body.Error != "" && body.Message != "":
				msg = body.Error + ": " + body.Message
			case body.Message != "":
				msg = body.Message
			case body.Error != "":
				msg = body.Error
			}
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return resp.StatusCode, fmt.Errorf("pkgregistry: %s: HTTP %d: %s", p.id, resp.StatusCode, msg)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("pkgregistry: %s: decode: %w", p.id, err)
	}
	return resp.StatusCode, nil
}

// skillsShSkill is the directory's listing/search shape.
type skillsShSkill struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Installs   int64  `json:"installs"`
	SourceType string `json:"sourceType"`
	InstallURL string `json:"installUrl"`
}

// Search implements pkgregistry.Provider.
func (p *skillsShProvider) Search(ctx context.Context, query string) ([]sdkpkg.Package, error) {
	queries := skillsShSearchQueries(query)
	var out []sdkpkg.Package
	var lastErr error
	seen := map[string]bool{}
	for i, q := range queries {
		results, err := p.searchOnce(ctx, q)
		if err != nil {
			lastErr = err
			if fallback := p.searchPublicCatalog(ctx, q); len(fallback) > 0 {
				for _, pkg := range fallback {
					key := strings.ToLower(pkg.Slug)
					if seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, pkg)
				}
				return out, nil
			}
			if i == 0 {
				return nil, err
			}
			continue
		}
		for _, pkg := range results {
			key := strings.ToLower(pkg.Slug)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, pkg)
		}
		if len(out) > 0 {
			return out, nil
		}
		if fallback := p.searchPublicCatalog(ctx, q); len(fallback) > 0 {
			for _, pkg := range fallback {
				key := strings.ToLower(pkg.Slug)
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, pkg)
			}
			return out, nil
		}
	}
	if lastErr != nil && len(out) == 0 {
		return nil, lastErr
	}
	return out, nil
}

func (p *skillsShProvider) searchPublicCatalog(ctx context.Context, query string) []sdkpkg.Package {
	if !strings.Contains(p.baseURL, "skills.sh") {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/", nil)
	if err != nil {
		return nil
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil
	}
	text := html.UnescapeString(string(raw))
	text = strings.ReplaceAll(text, `\"`, `"`)
	text = strings.ReplaceAll(text, `\u0026`, "&")
	return skillsShCatalogMatches(text, query, p.id)
}

var skillsShCatalogEntryRE = regexp.MustCompile(`"source":"([^"]+)","skillId":"([^"]+)","name":"([^"]+)","installs":([0-9]+)`)

func skillsShCatalogMatches(catalog, query, providerID string) []sdkpkg.Package {
	q := normalizeSkillsShQuery(query)
	if q == "" {
		return nil
	}
	matches := skillsShCatalogEntryRE.FindAllStringSubmatch(catalog, -1)
	out := make([]sdkpkg.Package, 0, 10)
	seen := map[string]bool{}
	for _, m := range matches {
		source, skillID, name, installs := m[1], m[2], m[3], m[4]
		slug := source + "/" + skillID
		haystack := normalizeSkillsShQuery(strings.Join([]string{source, skillID, name, slug}, " "))
		if !strings.Contains(haystack, q) {
			continue
		}
		key := strings.ToLower(slug)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, sdkpkg.Package{
			Slug:        slug,
			Version:     "latest",
			Source:      slug,
			Description: fmt.Sprintf("%s — %s installs (%s, public catalog fallback)", name, installs, source),
			Provider:    providerID,
		})
		if len(out) >= 25 {
			break
		}
	}
	return out
}

func normalizeSkillsShQuery(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("-", " ", "_", " ", "/", " ", ".", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func (p *skillsShProvider) searchOnce(ctx context.Context, query string) ([]sdkpkg.Package, error) {
	var body struct {
		Data []skillsShSkill `json:"data"`
	}
	u := p.baseURL + "/api/v1/skills/search?q=" + url.QueryEscape(query) + "&limit=25"
	status, err := p.getJSON(ctx, u, &body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("pkgregistry: %s: search returned HTTP %d", p.id, status)
	}
	out := make([]sdkpkg.Package, 0, len(body.Data))
	for _, s := range body.Data {
		out = append(out, sdkpkg.Package{
			Slug:        s.ID,
			Version:     "latest",
			Source:      s.ID,
			Description: fmt.Sprintf("%s — %d installs (%s)", s.Name, s.Installs, s.Source),
			Provider:    p.id,
		})
	}
	return out, nil
}

func skillsShSearchQueries(query string) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	variantSet := map[string]bool{}
	var variants []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || variantSet[strings.ToLower(v)] {
			return
		}
		variantSet[strings.ToLower(v)] = true
		variants = append(variants, v)
	}
	add(q)
	if strings.ContainsAny(q, "-_") {
		add(strings.NewReplacer("-", " ", "_", " ").Replace(q))
	}
	if strings.Contains(q, "/") {
		parts := strings.Split(q, "/")
		add(parts[len(parts)-1])
		add(strings.NewReplacer("-", " ", "_", " ").Replace(parts[len(parts)-1]))
	}
	return variants
}

// skillsShDetail is the detail endpoint's shape.
type skillsShDetail struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Slug     string `json:"slug"`
	Installs int64  `json:"installs"`
	Hash     string `json:"hash"`
	Files    []struct {
		Path     string `json:"path"`
		Contents string `json:"contents"`
	} `json:"files"`
}

func (p *skillsShProvider) detail(ctx context.Context, slug string) (skillsShDetail, error) {
	var d skillsShDetail
	status, err := p.getJSON(ctx, p.baseURL+"/api/v1/skills/"+slug, &d)
	if status == http.StatusNotFound {
		return d, fmt.Errorf("pkgregistry: %s: %q: %w", p.id, slug, sdkpkg.ErrNotFound)
	}
	if err != nil {
		return d, err
	}
	if status != http.StatusOK {
		return d, fmt.Errorf("pkgregistry: %s: detail returned HTTP %d", p.id, status)
	}
	return d, nil
}

// Resolve implements pkgregistry.Provider.
func (p *skillsShProvider) Resolve(ctx context.Context, slug string) (sdkpkg.Package, error) {
	d, err := p.detail(ctx, slug)
	if err != nil {
		return sdkpkg.Package{}, err
	}
	version := d.Hash
	if len(version) > 12 {
		version = version[:12]
	}
	if version == "" {
		version = "latest"
	}
	desc := fmt.Sprintf("%d installs", d.Installs)
	if audits := p.auditSummary(ctx, slug); audits != "" {
		desc += " · audits: " + audits
	}
	return sdkpkg.Package{
		Slug:        d.ID,
		Version:     version,
		Source:      d.ID,
		Description: desc,
		Provider:    p.id,
	}, nil
}

// auditSummary best-effort fetches the directory's partner security audits
// ("Gen Agent Trust Hub: pass (LOW), Socket: warn — 1 alert"). Errors and
// 404s (not yet audited) yield "" — audits inform consent, never block it;
// the host's own E20 introspection still runs on every install.
func (p *skillsShProvider) auditSummary(ctx context.Context, slug string) string {
	var body struct {
		Audits []struct {
			Provider  string `json:"provider"`
			Status    string `json:"status"`
			Summary   string `json:"summary"`
			RiskLevel string `json:"riskLevel"`
		} `json:"audits"`
	}
	status, err := p.getJSON(ctx, p.baseURL+"/api/v1/skills/audit/"+slug, &body)
	if err != nil || status != http.StatusOK {
		return ""
	}
	parts := make([]string, 0, len(body.Audits))
	for _, a := range body.Audits {
		s := a.Provider + ": " + a.Status
		if a.RiskLevel != "" {
			s += " (" + a.RiskLevel + ")"
		}
		if a.Status != "pass" && a.Summary != "" {
			s += " — " + a.Summary
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// Fetch implements pkgregistry.Provider: the file tree arrives inline from
// the detail endpoint and is materialised under dstDir (traversal-guarded).
func (p *skillsShProvider) Fetch(ctx context.Context, pkg sdkpkg.Package, dstDir string) error {
	slug := pkg.Source
	if slug == "" {
		slug = pkg.Slug
	}
	d, err := p.detail(ctx, slug)
	if err != nil {
		return err
	}
	if len(d.Files) == 0 {
		return fmt.Errorf("pkgregistry: %s: %q has no file snapshot yet", p.id, slug)
	}
	root, err := filepath.Abs(dstDir)
	if err != nil {
		return fmt.Errorf("pkgregistry: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("pkgregistry: %w", err)
	}
	for _, f := range d.Files {
		target := filepath.Join(root, filepath.FromSlash(f.Path))
		if rel, rerr := filepath.Rel(root, target); rerr != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("pkgregistry: %s: file %q escapes the destination", p.id, f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("pkgregistry: %w", err)
		}
		if err := os.WriteFile(target, []byte(f.Contents), 0o644); err != nil {
			return fmt.Errorf("pkgregistry: write %s: %w", f.Path, err)
		}
	}
	return nil
}
