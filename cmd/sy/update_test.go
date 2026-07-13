package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckForUpdateNoManifestConfigured(t *testing.T) {
	t.Setenv("SOULACY_UPDATE_MANIFEST", "")
	res, err := checkForUpdate(context.Background(), "", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if res.UpdateAvailable {
		t.Fatalf("update available = true for no manifest: %+v", res)
	}
	if res.ManifestSource != "" {
		t.Fatalf("manifest source = %q, want empty", res.ManifestSource)
	}
}

func TestCheckForUpdateFindsNewerManifestAndPlatformArtifact(t *testing.T) {
	path := writeUpdateManifest(t, updateManifest{
		Product: "soulacy",
		Version: "1.3.0",
		Artifacts: []updateArtifact{
			{Name: "other.tar.gz", OS: "plan9", Arch: "amd64", SHA256: "abc"},
			{Name: "current.tar.gz", OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: "def"},
		},
	})
	res, err := checkForUpdate(context.Background(), path, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if !res.UpdateAvailable {
		t.Fatalf("update available = false: %+v", res)
	}
	if res.Artifact == nil || res.Artifact.Name != "current.tar.gz" {
		t.Fatalf("artifact = %+v", res.Artifact)
	}
}

func TestReadUpdateManifestRejectsWrongProduct(t *testing.T) {
	path := writeUpdateManifest(t, updateManifest{Product: "other", Version: "1.2.3"})
	if _, err := readUpdateManifest(context.Background(), path); err == nil {
		t.Fatal("expected wrong product to error")
	}
}

func TestInstallUpdateDryRunVerifiesArtifactWithoutReplacing(t *testing.T) {
	dir := t.TempDir()
	archive := writeUpdateArchive(t, dir, "new soulacy", "new sy")
	sum := fileSHA256(t, archive)
	manifest := writeUpdateManifest(t, updateManifest{
		Product: "soulacy",
		Version: "1.3.0",
		Artifacts: []updateArtifact{{
			Name:   filepath.Base(archive),
			OS:     runtime.GOOS,
			Arch:   runtime.GOARCH,
			SHA256: sum,
			Bytes:  fileSize(t, archive),
			URL:    archive,
		}},
	})
	installDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "soulacy"), []byte("old soulacy"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := installUpdate(context.Background(), updateInstallOptions{
		ManifestSource: manifest,
		CurrentVersion: "1.2.0",
		InstallDir:     installDir,
		DryRun:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Installed {
		t.Fatalf("dry-run result = %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(installDir, "soulacy"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old soulacy" {
		t.Fatalf("dry-run replaced binary: %q", got)
	}
}

func TestInstallUpdateInstallsAndBacksUpBinaries(t *testing.T) {
	dir := t.TempDir()
	archive := writeUpdateArchive(t, dir, "new soulacy", "new sy")
	manifest := writeUpdateManifest(t, updateManifest{
		Product: "soulacy",
		Version: "1.3.0",
		Artifacts: []updateArtifact{{
			Name:   filepath.Base(archive),
			OS:     runtime.GOOS,
			Arch:   runtime.GOARCH,
			SHA256: fileSHA256(t, archive),
			Bytes:  fileSize(t, archive),
			URL:    archive,
		}},
	})
	installDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "soulacy"), []byte("old soulacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "sy"), []byte("old sy"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := installUpdate(context.Background(), updateInstallOptions{
		ManifestSource: manifest,
		CurrentVersion: "1.2.0",
		InstallDir:     installDir,
		Yes:            true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || len(res.Backups) != 2 {
		t.Fatalf("install result = %+v", res)
	}
	if got := readFileString(t, filepath.Join(installDir, "soulacy")); got != "new soulacy" {
		t.Fatalf("soulacy = %q", got)
	}
	if got := readFileString(t, filepath.Join(installDir, "sy")); got != "new sy" {
		t.Fatalf("sy = %q", got)
	}
	for _, backup := range res.Backups {
		if _, err := os.Stat(backup); err != nil {
			t.Fatalf("backup %s missing: %v", backup, err)
		}
	}
}

func TestInstallUpdateRejectsBadChecksum(t *testing.T) {
	dir := t.TempDir()
	archive := writeUpdateArchive(t, dir, "new soulacy", "new sy")
	manifest := writeUpdateManifest(t, updateManifest{
		Product: "soulacy",
		Version: "1.3.0",
		Artifacts: []updateArtifact{{
			Name:   filepath.Base(archive),
			OS:     runtime.GOOS,
			Arch:   runtime.GOARCH,
			SHA256: "not-the-real-sum",
			Bytes:  fileSize(t, archive),
			URL:    archive,
		}},
	})
	_, err := installUpdate(context.Background(), updateInstallOptions{
		ManifestSource: manifest,
		CurrentVersion: "1.2.0",
		InstallDir:     filepath.Join(dir, "bin"),
		DryRun:         true,
	})
	if err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func writeUpdateManifest(t *testing.T, manifest updateManifest) string {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release-manifest.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeUpdateArchive(t *testing.T, dir, soulacyBody, syBody string) string {
	t.Helper()
	path := filepath.Join(dir, "soulacy_1.3.0_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"soulacy": soulacyBody, "sy": syBody} {
		data := []byte(body)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
