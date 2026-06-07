package pkgregistry

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
	sdkpkg "github.com/soulacy/soulacy/sdk/pkgregistry"
	"github.com/soulacy/soulacy/sdk/registry"
)

// Entry pairs a provider with its resolution priority (LOWER runs first).
type Entry struct {
	Provider sdkpkg.Provider
	Priority int
}

// Engine queries multiple package registries in priority order with
// fallback semantics: Resolve returns the first hit; Search aggregates all
// providers and dedupes by slug keeping the highest-priority result; Fetch
// routes back to the provider that resolved the package.
type Engine struct {
	entries []Entry // sorted by Priority asc, stable on config order
	log     *zap.Logger
}

// NewEngine builds an Engine from provider entries (any order — sorted here).
func NewEngine(entries []Entry, log *zap.Logger) *Engine {
	if log == nil {
		log = zap.NewNop()
	}
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })
	return &Engine{entries: sorted, log: log}
}

// FromConfig resolves each `registries:` entry through the SDK factory
// registry (keyed by Type, default "http"). Unknown types and factory
// errors are returned as warnings — the remaining registries still work.
func FromConfig(cfgs []config.RegistryConfig, log *zap.Logger) (*Engine, []error) {
	var entries []Entry
	var errs []error
	for _, rc := range cfgs {
		typ := rc.Type
		if typ == "" {
			typ = "http"
		}
		id := rc.ID
		if id == "" {
			id = typ
		}
		m := map[string]any{
			"id":       id,
			"base_url": rc.BaseURL,
		}
		if len(rc.AuthHeaders) > 0 {
			ah := make(map[string]any, len(rc.AuthHeaders))
			for k, v := range rc.AuthHeaders {
				ah[k] = v
			}
			m["auth_headers"] = ah
		}
		p, ok, err := registry.NewPkgRegistry(typ, m)
		if !ok {
			errs = append(errs, fmt.Errorf("pkgregistry: unknown registry type %q for entry %q (registered: %v)", typ, id, registry.PkgRegistries()))
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("pkgregistry: entry %q: %w", id, err))
			continue
		}
		entries = append(entries, Entry{Provider: p, Priority: rc.Priority})
	}
	return NewEngine(entries, log), errs
}

// Providers lists provider IDs in resolution order.
func (e *Engine) Providers() []string {
	out := make([]string, len(e.entries))
	for i, en := range e.entries {
		out[i] = en.Provider.ID()
	}
	return out
}

// Resolve tries each registry in priority order and returns the first
// successful resolution (Provider field stamped). ErrNotFound and provider
// errors both fall through; if nothing resolves, the error wraps ErrNotFound
// so callers can distinguish "unknown slug" from transport trouble via the
// collected detail.
func (e *Engine) Resolve(ctx context.Context, slug string) (sdkpkg.Package, error) {
	var lastErr error
	for _, en := range e.entries {
		pkg, err := en.Provider.Resolve(ctx, slug)
		if err == nil {
			// Stamp the engine's routing key — Fetch routes on it, and the
			// consent dialog shows provenance.
			pkg.Provider = en.Provider.ID()
			return pkg, nil
		}
		if !errors.Is(err, sdkpkg.ErrNotFound) {
			e.log.Warn("package registry resolve failed; falling through",
				zap.String("registry", en.Provider.ID()), zap.String("slug", slug), zap.Error(err))
			lastErr = err
		}
	}
	if lastErr != nil {
		return sdkpkg.Package{}, fmt.Errorf("pkgregistry: %q not resolved (last error: %v): %w", slug, lastErr, sdkpkg.ErrNotFound)
	}
	return sdkpkg.Package{}, fmt.Errorf("pkgregistry: %q not found in any configured registry: %w", slug, sdkpkg.ErrNotFound)
}

// Search fans out to every provider in priority order, concatenates the
// results, and dedupes by slug (first/highest-priority hit wins). Provider
// errors are logged and skipped — one broken registry never hides the rest.
func (e *Engine) Search(ctx context.Context, query string) []sdkpkg.Package {
	var out []sdkpkg.Package
	seen := map[string]struct{}{}
	for _, en := range e.entries {
		results, err := en.Provider.Search(ctx, query)
		if err != nil {
			e.log.Warn("package registry search failed; skipping",
				zap.String("registry", en.Provider.ID()), zap.Error(err))
			continue
		}
		for _, pkg := range results {
			if _, dup := seen[pkg.Slug]; dup {
				continue
			}
			seen[pkg.Slug] = struct{}{}
			if pkg.Provider == "" {
				pkg.Provider = en.Provider.ID()
			}
			out = append(out, pkg)
		}
	}
	return out
}

// Fetch routes the download to the provider that resolved pkg (pkg.Provider).
func (e *Engine) Fetch(ctx context.Context, pkg sdkpkg.Package, dstDir string) error {
	for _, en := range e.entries {
		if en.Provider.ID() == pkg.Provider {
			return en.Provider.Fetch(ctx, pkg, dstDir)
		}
	}
	return fmt.Errorf("pkgregistry: no registry %q for package %q (configured: %v)", pkg.Provider, pkg.Slug, e.Providers())
}
