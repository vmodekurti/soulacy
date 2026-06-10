package studioplugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeedWritesPluginOnFirstRun(t *testing.T) {
	dir := t.TempDir()

	seeded, err := Seed(dir)
	if err != nil {
		t.Fatalf("Seed: unexpected error: %v", err)
	}
	if !seeded {
		t.Fatalf("Seed: expected seeded=true on first run, got false")
	}

	for _, rel := range []string{
		filepath.Join("studio", "plugin.yaml"),
		filepath.Join("studio", "ui", "index.html"),
	} {
		path := filepath.Join(dir, rel)
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("expected %s to exist: %v", rel, statErr)
		}
		if info.Size() == 0 {
			t.Fatalf("expected %s to be non-empty", rel)
		}
	}
}

func TestSeedIsAbsentOnly(t *testing.T) {
	dir := t.TempDir()

	if _, err := Seed(dir); err != nil {
		t.Fatalf("first Seed: %v", err)
	}

	manifest := filepath.Join(dir, "studio", "plugin.yaml")
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read seeded manifest: %v", err)
	}

	seeded, err := Seed(dir)
	if err != nil {
		t.Fatalf("second Seed: unexpected error: %v", err)
	}
	if seeded {
		t.Fatalf("second Seed: expected seeded=false, got true")
	}

	after, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("re-read manifest: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("second Seed modified the existing manifest")
	}
}

func TestSeedEmptyDirIsNoop(t *testing.T) {
	seeded, err := Seed("")
	if err != nil {
		t.Fatalf("Seed(\"\"): unexpected error: %v", err)
	}
	if seeded {
		t.Fatalf("Seed(\"\"): expected seeded=false, got true")
	}
}
