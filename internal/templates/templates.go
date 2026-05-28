// Package templates serves agent starter templates — pre-built SOUL.yaml
// definitions a user can clone with one click to get a working agent without
// starting from a blank file. Inspired by Langflow's "New Project from
// Template" flow: the magic in those product demos is that the user lands on
// a wired-up flow in ~5 seconds.
//
// Discovery order (later entries take precedence):
//   1. Embedded defaults — shipped with the binary so the gateway works
//      out-of-the-box. Located in ./embedded/*.yaml relative to this file.
//   2. User dir (optional) — every *.yaml in the configured user templates
//      dir (default ~/.soulacy/templates). Same-name files override the
//      embedded ones, so a user can replace any default by dropping a file
//      with the matching basename.
//
// Each template is a regular SOUL.yaml. Its `id`, `name`, and `description`
// fields double as the template's display metadata — no separate manifest.
// The `id` is rewritten on instantiation so multiple instances can coexist.
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/pkg/agent"
)

//go:embed embedded/*.yaml
var embedded embed.FS

// Catalog scans embedded defaults + the user dir and returns the merged set.
// Safe to call repeatedly; results are NOT cached because users may drop new
// files into the user dir while the gateway is running and expect them to
// show up without a restart (mirrors the agent-loader hot-reload behaviour).
type Catalog struct {
	userDir string
}

// New creates a Catalog. userDir may be empty (only embedded defaults will
// be returned in that case).
func New(userDir string) *Catalog {
	return &Catalog{userDir: userDir}
}

// Entry is a single template's metadata + the raw YAML bytes. The bytes are
// returned alongside the parsed Definition so callers can either write the
// file verbatim (after ID rewriting) or use the parsed form for inspection.
type Entry struct {
	Name        string             `json:"name"`        // basename without extension, the public handle
	DisplayName string             `json:"display_name"` // from Definition.Name
	Description string             `json:"description"`  // from Definition.Description
	Tags        []string           `json:"tags"`
	Source      string             `json:"source"`       // "embedded" | "user"
	Definition  *agent.Definition  `json:"definition"`   // parsed (for previews — engine isn't going to run this)
}

// List returns all templates sorted by Name, with user-dir entries shadowing
// embedded ones of the same name.
func (c *Catalog) List() ([]Entry, error) {
	out := map[string]Entry{}

	// Embedded first.
	embEntries, err := readEmbedded()
	if err != nil {
		return nil, err
	}
	for _, e := range embEntries {
		out[e.Name] = e
	}

	// User overrides.
	if c.userDir != "" {
		userEntries, err := readUserDir(c.userDir)
		if err != nil {
			return nil, fmt.Errorf("read user templates: %w", err)
		}
		for _, e := range userEntries {
			out[e.Name] = e
		}
	}

	entries := make([]Entry, 0, len(out))
	for _, e := range out {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// Get returns one template by name (case-sensitive, matches the file basename
// without extension).
func (c *Catalog) Get(name string) (*Entry, error) {
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("template %q not found", name)
}

// readEmbedded parses every *.yaml under embedded/.
func readEmbedded() ([]Entry, error) {
	dir := "embedded"
	files, err := fs.ReadDir(embedded, dir)
	if err != nil {
		return nil, fmt.Errorf("read embedded dir: %w", err)
	}
	var out []Entry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name(), ".yaml") && !strings.HasSuffix(f.Name(), ".yml") {
			continue
		}
		data, err := embedded.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, err
		}
		e, err := parseEntry(f.Name(), data, "embedded")
		if err != nil {
			// Skip a broken embedded template rather than crashing the
			// whole catalog — a build-time test will catch it before ship.
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// readUserDir parses every *.yaml in the user templates dir (non-recursive).
// A missing dir is fine — just returns an empty list.
func readUserDir(dir string) ([]Entry, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name(), ".yaml") && !strings.HasSuffix(f.Name(), ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			continue // tolerate transient read failures
		}
		e, err := parseEntry(f.Name(), data, "user")
		if err != nil {
			continue // tolerate user-broken templates rather than 500'ing
		}
		out = append(out, e)
	}
	return out, nil
}

func parseEntry(filename string, data []byte, source string) (Entry, error) {
	var def agent.Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return Entry{}, fmt.Errorf("parse %s: %w", filename, err)
	}
	base := strings.TrimSuffix(strings.TrimSuffix(filename, ".yml"), ".yaml")
	return Entry{
		Name:        base,
		DisplayName: def.Name,
		Description: def.Description,
		Tags:        def.Tags,
		Source:      source,
		Definition:  &def,
	}, nil
}

// Instantiate clones the named template into a fresh Definition. The caller
// supplies the desired agent ID; if it's empty, the catalog derives one from
// the template name (e.g. "rag-over-docs"). The mustBeUnique predicate is
// applied to suffix-bump the ID until it returns true; callers should pass
// `func(id string) bool { return loader.Get(id) == nil }` so live agents
// aren't overwritten.
//
// The returned Definition is ready to hand to Loader.Upsert — SourcePath is
// blank so a fresh folder is created.
func (c *Catalog) Instantiate(templateName, desiredID string, mustBeUnique func(string) bool) (*agent.Definition, error) {
	entry, err := c.Get(templateName)
	if err != nil {
		return nil, err
	}
	if entry.Definition == nil {
		return nil, fmt.Errorf("template %q has no parsed definition", templateName)
	}

	// Shallow-copy so the catalog's cached Definition is not mutated. Slices
	// and maps are shared by reference, but the engine treats Definition
	// fields as read-only, so this is safe in the same way Loader.Get is.
	def := *entry.Definition

	// Pick a unique ID.
	base := strings.TrimSpace(desiredID)
	if base == "" {
		// Strip the "-template" suffix the embedded files use, so a fresh
		// instance lands at the natural name ("rag-over-docs" not
		// "rag-over-docs-template").
		base = strings.TrimSuffix(templateName, "-template")
	}
	def.ID = uniqueID(base, mustBeUnique)
	def.SourcePath = "" // force a new folder under <dir>/<id>/SOUL.yaml

	return &def, nil
}

func uniqueID(base string, mustBeUnique func(string) bool) string {
	if mustBeUnique == nil || mustBeUnique(base) {
		return base
	}
	for i := 2; i < 1_000_000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if mustBeUnique(candidate) {
			return candidate
		}
	}
	return base // pathological — give up and let the loader complain
}

// DefaultUserDir returns the conventional location for user-supplied
// templates. The gateway is free to override via config.
func DefaultUserDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".soulacy", "templates")
}

