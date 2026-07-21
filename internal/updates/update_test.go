package updates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"v1.2.3", "1.2.0", 1},
		{"1.2.3-alpha", "1.2.3", 0},
		{"dev", "1.0.0", 0},
	}
	for _, tt := range tests {
		got := CompareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("CompareSemver(%q, %q) = %d; want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCheckForUpdateCustomManifest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		manifest := UpdateManifest{
			Product: "soulacy",
			Version: "1.2.3",
			Artifacts: []UpdateArtifact{
				{
					Name:   "soulacy_1.2.3_darwin_arm64.tar.gz",
					OS:     "darwin",
					Arch:   "arm64",
					SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
					Bytes:  0,
				},
			},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer ts.Close()

	res, err := CheckForUpdate(context.Background(), ts.URL, "1.0.0")
	if err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if !res.UpdateAvailable {
		t.Errorf("expected update to be available")
	}
	if res.LatestVersion != "1.2.3" {
		t.Errorf("got latest version %s, want 1.2.3", res.LatestVersion)
	}
}

func TestInstallUpdateDryRun(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "soulacy-update-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	_ = os.WriteFile(filepath.Join(tempDir, "soulacy"), []byte("current-soulacy"), 0o755)
	_ = os.WriteFile(filepath.Join(tempDir, "sy"), []byte("current-sy"), 0o755)

	tarGzData, expectedSHA, err := createMockTarGz()
	if err != nil {
		t.Fatalf("failed to create mock tar.gz: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" || r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			manifest := UpdateManifest{
				Product: "soulacy",
				Version: "2.0.0",
				Artifacts: []UpdateArtifact{
					{
						Name:   "soulacy_2.0.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz",
						OS:     runtime.GOOS,
						Arch:   runtime.GOARCH,
						SHA256: expectedSHA,
						Bytes:  int64(len(tarGzData)),
						URL:    "archive.tar.gz",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}

		if r.URL.Path == "/archive.tar.gz" {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(tarGzData)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	opts := UpdateInstallOptions{
		ManifestSource: ts.URL + "/manifest.json",
		CurrentVersion: "1.0.0",
		InstallDir:     tempDir,
		DryRun:         true,
		Yes:            true,
	}

	res, err := InstallUpdate(context.Background(), opts)
	if err != nil {
		t.Fatalf("InstallUpdate: %v", err)
	}
	if !res.UpdateAvailable {
		t.Errorf("expected update available")
	}
}

func createMockTarGz() ([]byte, string, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, name := range []string{"soulacy", "sy"} {
		hdr := &tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(name)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write([]byte(name)); err != nil {
			return nil, "", err
		}
	}
	_ = tw.Close()
	_ = gw.Close()

	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}
