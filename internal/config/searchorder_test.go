package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The workspace config must ALWAYS win over a config.yaml in the current
// directory. (Regression: a repo checkout's dev config silently hijacked a
// fresh install whenever the gateway was launched from the repo — old
// example agents reappeared after a full wipe.)
func TestLoad_WorkspaceConfigBeatsCwdConfig(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("SOULACY_WORKSPACE", ws)

	if err := os.WriteFile(filepath.Join(ws, "config.yaml"),
		[]byte("server:\n  port: 11111\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "config.yaml"),
		[]byte("server:\n  port: 22222\nagent_dirs:\n  - examples/agents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	cfg, path, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 11111 {
		t.Errorf("port = %d — the CWD config shadowed the workspace config (loaded %s)",
			cfg.Server.Port, path)
	}
	if len(cfg.AgentDirs) > 0 && cfg.AgentDirs[0] == "examples/agents" {
		t.Errorf("agent_dirs came from the CWD config: %v", cfg.AgentDirs)
	}
}

// With no workspace or home config present, a CWD config still works
// (last-resort dev fallback).
func TestLoad_CwdConfigIsLastResort(t *testing.T) {
	ws := t.TempDir() // empty workspace — no config.yaml
	t.Setenv("SOULACY_WORKSPACE", ws)
	t.Setenv("HOME", t.TempDir())

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "config.yaml"),
		[]byte("server:\n  port: 33333\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	cfg, _, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 33333 {
		t.Errorf("port = %d, want the CWD fallback 33333", cfg.Server.Port)
	}
}
