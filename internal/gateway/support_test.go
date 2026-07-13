package gateway

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewaySupportBundleDownloadsRedactedZip(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agents")
	logDir := filepath.Join(root, "logs")
	cfgPath := filepath.Join(root, "config.yaml")
	if err := os.MkdirAll(filepath.Join(agentDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("server:\n  api_key: top-secret-api-key-value-1234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "demo", "SOUL.yaml"), []byte("id: demo\nbot_token: xoxb-agent-secret-token-1234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "demo.log"), []byte("token=xoxb-log-secret-token-value-abcdefghijklmnopqrstuvwxyz"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newTestGatewayWithCfgPath(t, "secret", cfgPath)
	s.cfg.AgentDirs = []string{agentDir}
	s.cfg.Log.File = filepath.Join(logDir, "soulacy.log")

	req, err := http.NewRequest(http.MethodGet, "/api/v1/support/bundle", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := s.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, string(raw))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/zip") {
		t.Fatalf("content-type = %q", ct)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	names := map[string]bool{}
	var joined strings.Builder
	for _, f := range zr.File {
		names[f.Name] = true
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		joined.Write(body)
	}
	for _, want := range []string{"manifest.json", "doctor.json", "readiness.json", "release.json", "config.redacted.yaml", "agents/demo.SOUL.redacted.yaml"} {
		if !names[want] {
			t.Fatalf("bundle missing %s; got %#v", want, names)
		}
	}
	all := joined.String()
	for _, forbidden := range []string{"top-secret-api-key", "xoxb-agent-secret", "xoxb-log-secret"} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("support bundle leaked %q:\n%s", forbidden, all)
		}
	}
}
