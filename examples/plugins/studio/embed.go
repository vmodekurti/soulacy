// Package studioplugin bundles the Studio plugin into the gateway binary and
// seeds it into the default plugins directory on first run, so the plugin shows
// up in the web portal with zero operator configuration.
//
// Studio is a bundled, default-on plugin. It is seeded exactly once: Seed only
// writes the plugin files when <pluginsDir>/studio does not already exist, and
// never clobbers an existing copy. Seeding the raw plugin files (manifest + ui)
// without a .soulacy-install.json marks it UNMANAGED, so it loads with no
// approval step (see internal/plugininstall.Gate).
//
// KNOWN FOLLOW-UP: because seeding is absent-only, upgrading the gateway binary
// does NOT re-seed an updated Studio over a previously-seeded copy. A future
// change should version the seeded payload and refresh it on upgrade.
package studioplugin

import (
	"embed"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// files holds the embedded Studio plugin payload: the manifest, README and the
// built static UI assets. The all: prefix includes dotfiles and nested assets.
//
// Building this package (and cmd/soulacy) therefore requires ui/ to exist —
// run `make plugin-ui` first. This mirrors the internal/webui/dist GUI embed.
//
//go:embed plugin.yaml README.md all:ui
var files embed.FS

// Seed writes the bundled Studio plugin into pluginsDir/studio if it is not
// already present, returning seeded=true when files were written.
//
// It is a no-op (false, nil) when pluginsDir is empty or when
// <pluginsDir>/studio already exists — an existing copy is never overwritten.
func Seed(pluginsDir string) (seeded bool, err error) {
	if pluginsDir == "" {
		return false, nil
	}

	dest := filepath.Join(pluginsDir, "studio")
	if _, statErr := os.Stat(dest); statErr == nil {
		// Already seeded (or operator-managed). Never clobber.
		return false, nil
	} else if !os.IsNotExist(statErr) {
		return false, statErr
	}

	walkErr := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return mkErr
		}
		return writeFile(p, target)
	})
	if walkErr != nil {
		return false, walkErr
	}
	return true, nil
}

// writeFile copies one embedded file at src (an fs path) to target on disk.
func writeFile(src, target string) error {
	in, err := files.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
