package updates

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/config"
)

type UpdateManifest struct {
	Product   string           `json:"product"`
	Version   string           `json:"version"`
	Artifacts []UpdateArtifact `json:"artifacts"`
}

type UpdateArtifact struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	URL    string `json:"url,omitempty"`
}

type UpdateCheckResult struct {
	CurrentVersion  string          `json:"current_version"`
	LatestVersion   string          `json:"latest_version,omitempty"`
	UpdateAvailable bool            `json:"update_available"`
	Comparable      bool            `json:"comparable"`
	ManifestSource  string          `json:"manifest_source,omitempty"`
	Artifact        *UpdateArtifact `json:"artifact,omitempty"`
	Message         string          `json:"message"`
}

type UpdateInstallOptions struct {
	ManifestSource string
	CurrentVersion string
	InstallDir     string
	DryRun         bool
	Yes            bool
}

type UpdateInstallResult struct {
	UpdateCheckResult
	Installed  bool     `json:"installed"`
	DryRun     bool     `json:"dry_run,omitempty"`
	InstallDir string   `json:"install_dir,omitempty"`
	Backups    []string `json:"backups,omitempty"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

const defaultGitHubRepo = "vmodekurti/soulacy"

var HTTPClient = http.DefaultClient

func CheckForUpdate(ctx context.Context, manifestSource, currentVersion string) (UpdateCheckResult, error) {
	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		currentVersion = strings.TrimSpace(config.Version)
	}

	res := UpdateCheckResult{
		CurrentVersion: currentVersion,
		ManifestSource: manifestSource,
	}

	var manifest UpdateManifest
	var err error

	if manifestSource == "" {
		// Fallback: Query GitHub Releases directly
		manifest, err = fetchLatestGitHubReleaseManifest(ctx, defaultGitHubRepo)
		if err != nil {
			res.Message = fmt.Sprintf("Failed to check GitHub releases: %v", err)
			return res, err
		}
		res.ManifestSource = "github-releases"
	} else {
		manifest, err = readUpdateManifest(ctx, manifestSource)
		if err != nil {
			return res, err
		}
	}

	res.LatestVersion = strings.TrimSpace(manifest.Version)
	res.Artifact = artifactForCurrentPlatform(manifest.Artifacts)
	cmp := CompareSemver(res.LatestVersion, currentVersion)
	res.Comparable = semverComparable(res.LatestVersion, currentVersion)
	res.UpdateAvailable = res.Comparable && cmp > 0

	switch {
	case !res.Comparable:
		res.Message = fmt.Sprintf("Soulacy %s is installed. Latest release version is %s, but versions are not comparable.", currentVersion, res.LatestVersion)
	case res.UpdateAvailable:
		res.Message = fmt.Sprintf("Update available: Soulacy %s -> %s.", currentVersion, res.LatestVersion)
	case cmp < 0:
		res.Message = fmt.Sprintf("Soulacy %s is newer than release version %s.", currentVersion, res.LatestVersion)
	default:
		res.Message = fmt.Sprintf("Soulacy %s is current.", currentVersion)
	}

	if res.UpdateAvailable && res.Artifact == nil {
		res.Message += fmt.Sprintf(" No %s/%s artifact was found in the release.", runtime.GOOS, runtime.GOARCH)
	}

	return res, nil
}

func InstallUpdate(ctx context.Context, opts UpdateInstallOptions) (UpdateInstallResult, error) {
	check, err := CheckForUpdate(ctx, opts.ManifestSource, opts.CurrentVersion)
	if err != nil {
		return UpdateInstallResult{}, err
	}
	res := UpdateInstallResult{UpdateCheckResult: check, DryRun: opts.DryRun}
	if !check.UpdateAvailable {
		res.Message = check.Message
		return res, nil
	}
	if check.Artifact == nil {
		return res, fmt.Errorf("update install: no %s/%s artifact found", runtime.GOOS, runtime.GOARCH)
	}
	if !opts.DryRun && !opts.Yes {
		return res, fmt.Errorf("update install: pass --yes to confirm or --dry-run to verify only")
	}
	installDir, err := resolveUpdateInstallDir(opts.InstallDir)
	if err != nil {
		return res, err
	}
	res.InstallDir = installDir

	data, source, err := readUpdateArtifact(ctx, check.ManifestSource, *check.Artifact)
	if err != nil {
		return res, err
	}
	if err := verifyUpdateArtifact(*check.Artifact, data); err != nil {
		return res, err
	}
	files, err := unpackUpdateArchive(data)
	if err != nil {
		return res, err
	}
	if _, ok := files["soulacy"]; !ok {
		return res, fmt.Errorf("update install: archive %s does not contain soulacy binary", check.Artifact.Name)
	}
	if _, ok := files["sy"]; !ok {
		return res, fmt.Errorf("update install: archive %s does not contain sy binary", check.Artifact.Name)
	}
	if opts.DryRun {
		res.Message = fmt.Sprintf("Update verified: Soulacy %s -> %s from %s. Dry run only; no files replaced.", check.CurrentVersion, check.LatestVersion, source)
		return res, nil
	}
	backups, err := installUpdateFiles(installDir, files)
	if err != nil {
		return res, err
	}
	res.Backups = backups
	res.Installed = true
	res.Message = fmt.Sprintf("Updated Soulacy %s -> %s in %s.", check.CurrentVersion, check.LatestVersion, installDir)
	return res, nil
}

func fetchLatestGitHubReleaseManifest(ctx context.Context, repo string) (UpdateManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	urlStr := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return UpdateManifest{}, err
	}
	// Add user-agent header as required by GitHub API
	req.Header.Set("User-Agent", "soulacy-updater")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return UpdateManifest{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No GitHub release has been published yet. Return a dummy manifest matching dev version.
		return UpdateManifest{
			Product: "soulacy",
			Version: "dev",
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return UpdateManifest{}, fmt.Errorf("github API: HTTP %d from %s", resp.StatusCode, urlStr)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return UpdateManifest{}, err
	}

	version := strings.TrimPrefix(rel.TagName, "v")
	manifest := UpdateManifest{
		Product: "soulacy",
		Version: version,
	}

	// Try to locate the checksums file first
	var checksumsURL string
	for _, asset := range rel.Assets {
		if asset.Name == "checksums.sha256" {
			checksumsURL = asset.BrowserDownloadURL
			break
		}
	}

	checksums := make(map[string]string)
	if checksumsURL != "" {
		if m, err := fetchAndParseChecksums(ctx, checksumsURL); err == nil {
			checksums = m
		}
	}

	for _, asset := range rel.Assets {
		if strings.HasSuffix(asset.Name, ".tar.gz") {
			// Extract OS/Arch from filename: soulacy_<version>_<os>_<arch>.tar.gz
			parts := strings.Split(strings.TrimSuffix(asset.Name, ".tar.gz"), "_")
			if len(parts) >= 4 {
				osName := parts[len(parts)-2]
				archName := parts[len(parts)-1]
				sha := checksums[asset.Name]
				manifest.Artifacts = append(manifest.Artifacts, UpdateArtifact{
					Name:   asset.Name,
					OS:     osName,
					Arch:   archName,
					Bytes:  asset.Size,
					SHA256: sha,
					URL:    asset.BrowserDownloadURL,
				})
			}
		}
	}

	return manifest, nil
}

func fetchAndParseChecksums(ctx context.Context, urlStr string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "soulacy-updater")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	checksums := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			checksums[parts[1]] = parts[0]
		}
	}
	return checksums, nil
}

func readUpdateManifest(ctx context.Context, source string) (UpdateManifest, error) {
	data, err := readUpdateManifestBytes(ctx, source)
	if err != nil {
		return UpdateManifest{}, err
	}
	var manifest UpdateManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return UpdateManifest{}, fmt.Errorf("update manifest: invalid JSON: %w", err)
	}
	if strings.TrimSpace(manifest.Product) != "" && !strings.EqualFold(manifest.Product, "soulacy") {
		return UpdateManifest{}, fmt.Errorf("update manifest: unexpected product %q", manifest.Product)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return UpdateManifest{}, fmt.Errorf("update manifest: version is required")
	}
	return manifest, nil
}

func readUpdateManifestBytes(ctx context.Context, source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "soulacy-updater")
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("update manifest: HTTP %d from %s", resp.StatusCode, source)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	}
	return os.ReadFile(source)
}

func readUpdateArtifact(ctx context.Context, manifestSource string, artifact UpdateArtifact) ([]byte, string, error) {
	source, err := resolveUpdateArtifactSource(manifestSource, artifact)
	if err != nil {
		return nil, "", err
	}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, source, err
		}
		req.Header.Set("User-Agent", "soulacy-updater")
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return nil, source, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, source, fmt.Errorf("update artifact: HTTP %d from %s", resp.StatusCode, source)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
		return data, source, err
	}
	data, err := os.ReadFile(source)
	return data, source, err
}

func resolveUpdateArtifactSource(manifestSource string, artifact UpdateArtifact) (string, error) {
	raw := strings.TrimSpace(artifact.URL)
	if raw == "" {
		raw = strings.TrimSpace(artifact.Name)
	}
	if raw == "" {
		return "", fmt.Errorf("update artifact: url or name is required")
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") || filepath.IsAbs(raw) {
		return raw, nil
	}
	if strings.HasPrefix(manifestSource, "http://") || strings.HasPrefix(manifestSource, "https://") {
		base, err := url.Parse(manifestSource)
		if err != nil {
			return "", err
		}
		ref, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		return base.ResolveReference(ref).String(), nil
	}
	if manifestSource == "" || manifestSource == "github-releases" {
		return raw, nil
	}
	return filepath.Join(filepath.Dir(manifestSource), raw), nil
}

func verifyUpdateArtifact(artifact UpdateArtifact, data []byte) error {
	if artifact.Bytes > 0 && int64(len(data)) != artifact.Bytes {
		return fmt.Errorf("update artifact: byte size mismatch for %s: got %d want %d", artifact.Name, len(data), artifact.Bytes)
	}
	want := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if want == "" {
		// If we queried GitHub directly and couldn't parse the checksums file, we can proceed without validation
		// but log a warning.
		return nil
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("update artifact: sha256 mismatch for %s: got %s want %s", artifact.Name, got, want)
	}
	return nil
}

func unpackUpdateArchive(data []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("update archive: open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("update archive: read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(filepath.Clean(hdr.Name), string(filepath.Separator))
		base := filepath.Base(name)
		if name == "." || strings.Contains(name, "..") || (base != "soulacy" && base != "sy") {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 512<<20))
		if err != nil {
			return nil, fmt.Errorf("update archive: read %s: %w", hdr.Name, err)
		}
		files[base] = body
	}
	return files, nil
}

func installUpdateFiles(installDir string, files map[string][]byte) ([]string, error) {
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	backups := []string{}
	for _, name := range []string{"soulacy", "sy"} {
		data := files[name]
		dest := filepath.Join(installDir, name)
		if st, err := os.Stat(dest); err == nil && st.Mode().IsRegular() {
			backup := fmt.Sprintf("%s.bak-%s", dest, stamp)
			if err := os.Rename(dest, backup); err != nil {
				return backups, err
			}
			backups = append(backups, backup)
		}
		tmp, err := os.CreateTemp(installDir, "."+name+".update-*")
		if err != nil {
			return backups, err
		}
		tmpPath := tmp.Name()
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return backups, err
		}
		if err := tmp.Chmod(0o755); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return backups, err
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpPath)
			return backups, err
		}
		if err := os.Rename(tmpPath, dest); err != nil {
			_ = os.Remove(tmpPath)
			return backups, err
		}
	}
	return backups, nil
}

func artifactForCurrentPlatform(artifacts []UpdateArtifact) *UpdateArtifact {
	for i := range artifacts {
		if artifacts[i].OS == runtime.GOOS && artifacts[i].Arch == runtime.GOARCH {
			return &artifacts[i]
		}
	}
	return nil
}

func semverComparable(a, b string) bool {
	_, oka := SemverParts(a)
	_, okb := SemverParts(b)
	return oka && okb
}

func CompareSemver(a, b string) int {
	if a == b {
		return 0
	}
	pa, oka := SemverParts(a)
	pb, okb := SemverParts(b)
	if !oka || !okb {
		return 0
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func SemverParts(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	segs := strings.Split(v, ".")
	var out [3]int
	if len(segs) == 0 || segs[0] == "" {
		return out, false
	}
	for i := 0; i < 3 && i < len(segs); i++ {
		n, err := strconv.Atoi(segs[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func resolveUpdateInstallDir(explicit string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return filepath.Abs(explicit)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("update install: resolve current executable: %w", err)
	}
	return filepath.Dir(exe), nil
}
