package supportbundle

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactedYAMLFileMasksSecretKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("llm:\n  providers:\n    openai:\n      api_key: sk-test-secret-value-1234567890\nplain: visible\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := RedactedYAMLFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "sk-test-secret-value") {
		t.Fatalf("secret leaked in redacted yaml:\n%s", got)
	}
	if !strings.Contains(got, "plain: visible") || !strings.Contains(got, "***REDACTED***") {
		t.Fatalf("unexpected redacted yaml:\n%s", got)
	}
}

func TestWriteIncludesRedactedConfigAndManifest(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config.yaml")
	agents := filepath.Join(root, "agents")
	logs := filepath.Join(root, "logs")
	if err := os.MkdirAll(filepath.Join(agents, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("channels:\n  slack:\n    bot_token: xoxb-super-secret-token-value-1234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "demo", "SOUL.yaml"), []byte("id: demo\nllm:\n  api_key: supersecretvalueabcdefghijklmnopqrstuvwxyz0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "demo.log"), []byte("token=supersecretvalueabcdefghijklmnopqrstuvwxyz0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	_, err := Write(&buf, Options{
		ConfigPath: cfg,
		AgentDirs:  []string{agents},
		LogDirs:    []string{logs},
		Workspace:  map[string]string{"root": root, "agents": agents, "logs": logs},
		Doctor:     map[string]any{"ok": true},
		Now:        time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	var all strings.Builder
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		all.Write(data)
	}
	for _, want := range []string{"manifest.json", "doctor.json", "config.redacted.yaml", "agents/demo.SOUL.redacted.yaml"} {
		if !names[want] {
			t.Fatalf("bundle missing %s; got %#v", want, names)
		}
	}
	joined := all.String()
	for _, forbidden := range []string{"xoxb-super-secret", "supersecretvalueabcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("bundle leaked %q:\n%s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "***REDACTED***") {
		t.Fatalf("bundle did not include redaction marker:\n%s", joined)
	}
}
