package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExplicitConfigKeepsHomeBackedDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
server:
  api_key: test-key
memory:
  max_history: 7
`), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, resolved, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if resolved != cfgPath {
		t.Fatalf("resolved path = %q, want %q", resolved, cfgPath)
	}

	wantMemoryDir := filepath.Join(home, ".soulacy", "memory")
	if cfg.Memory.Dir != wantMemoryDir {
		t.Fatalf("memory.dir = %q, want %q", cfg.Memory.Dir, wantMemoryDir)
	}
	wantArchive := filepath.Join(home, ".soulacy", "archive.db")
	if cfg.Memory.SQLitePath != wantArchive {
		t.Fatalf("memory.sqlite_path = %q, want %q", cfg.Memory.SQLitePath, wantArchive)
	}
	wantKB := filepath.Join(home, ".soulacy", "knowledge.db")
	if cfg.Knowledge.DBPath != wantKB {
		t.Fatalf("knowledge.db_path = %q, want %q", cfg.Knowledge.DBPath, wantKB)
	}
	if len(cfg.AgentDirs) != 1 || cfg.AgentDirs[0] != filepath.Join(home, ".soulacy", "agents") {
		t.Fatalf("agent_dirs = %#v", cfg.AgentDirs)
	}
	if cfg.Memory.MaxHistory != 7 {
		t.Fatalf("memory.max_history = %d, want 7", cfg.Memory.MaxHistory)
	}
}
