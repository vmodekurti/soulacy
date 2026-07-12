package main

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
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/soulacy/soulacy/internal/config"
)

type updateManifest struct {
	Product   string           `json:"product"`
	Version   string           `json:"version"`
	Artifacts []updateArtifact `json:"artifacts"`
}

type updateArtifact struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	URL    string `json:"url,omitempty"`
}

type updateCheckResult struct {
	CurrentVersion  string          `json:"current_version"`
	LatestVersion   string          `json:"latest_version,omitempty"`
	UpdateAvailable bool            `json:"update_available"`
	Comparable      bool            `json:"comparable"`
	ManifestSource  string          `json:"manifest_source,omitempty"`
	Artifact        *updateArtifact `json:"artifact,omitempty"`
	Message         string          `json:"message"`
}

type updateInstallResult struct {
	updateCheckResult
	Installed  bool     `json:"installed"`
	DryRun     bool     `json:"dry_run,omitempty"`
	InstallDir string   `json:"install_dir,omitempty"`
	Backups    []string `json:"backups,omitempty"`
}

func buildUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check and install release updates",
	}
	cmd.AddCommand(buildUpdateCheckCmd())
	cmd.AddCommand(buildUpdateInstallCmd())
	return cmd
}

func buildUpdateCheckCmd() *cobra.Command {
	var manifestSource string
	var currentVersion string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check whether a newer Soulacy release is available",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := checkForUpdate(cmd.Context(), manifestSource, currentVersion)
			if err != nil {
				return err
			}
			if outputJSON {
				data, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			fmt.Println(res.Message)
			if res.Artifact != nil {
				fmt.Printf("Artifact: %s (%s/%s, sha256 %s)\n", res.Artifact.Name, res.Artifact.OS, res.Artifact.Arch, res.Artifact.SHA256)
				if res.Artifact.URL != "" {
					fmt.Println("URL:", res.Artifact.URL)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestSource, "manifest", "", "Release manifest URL or local file path")
	cmd.Flags().StringVar(&currentVersion, "current", "", "Override current version for testing")
	return cmd
}

func buildUpdateInstallCmd() *cobra.Command {
	var manifestSource string
	var currentVersion string
	var installDir string
	var dryRun bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download, verify, and install a newer Soulacy release",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := installUpdate(cmd.Context(), updateInstallOptions{
				ManifestSource: manifestSource,
				CurrentVersion: currentVersion,
				InstallDir:     installDir,
				DryRun:         dryRun,
				Yes:            yes,
			})
			if err != nil {
				return err
			}
			if outputJSON {
				data, _ := json.MarshalIndent(res, "", "  ")
				fmt.Println(string(data))
				return nil
			}
			fmt.Println(res.Message)
			if len(res.Backups) > 0 {
				fmt.Println("Backups:")
				for _, b := range res.Backups {
					fmt.Println(" -", b)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestSource, "manifest", "", "Release manifest URL or local file path")
	cmd.Flags().StringVar(&currentVersion, "current", "", "Override current version for testing")
	cmd.Flags().StringVar(&installDir, "install-dir", "", "Directory to install soulacy and sy into (default: current executable directory)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Verify what would be installed without replacing binaries")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm installation without an interactive prompt")
	return cmd
}

func checkForUpdate(ctx context.Context, manifestSource, currentVersion string) (updateCheckResult, error) {
	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		currentVersion = strings.TrimSpace(config.Version)
	}
	manifestSource = resolveUpdateManifestSource(manifestSource)
	res := updateCheckResult{
		CurrentVersion: currentVersion,
		ManifestSource: manifestSource,
	}
	if manifestSource == "" {
		res.Message = fmt.Sprintf("Soulacy %s is installed. No update manifest is configured.", currentVersion)
		return res, nil
	}
	manifest, err := readUpdateManifest(ctx, manifestSource)
	if err != nil {
		return res, err
	}
	res.LatestVersion = strings.TrimSpace(manifest.Version)
	res.Artifact = artifactForCurrentPlatform(manifest.Artifacts)
	cmp := compareSemver(res.LatestVersion, currentVersion)
	res.Comparable = semverComparable(res.LatestVersion, currentVersion)
	res.UpdateAvailable = res.Comparable && cmp > 0
	switch {
	case !res.Comparable:
		res.Message = fmt.Sprintf("Soulacy %s is installed. Latest manifest version is %s, but one version is not semver-comparable.", currentVersion, res.LatestVersion)
	case res.UpdateAvailable:
		res.Message = fmt.Sprintf("Update available: Soulacy %s -> %s.", currentVersion, res.LatestVersion)
	case cmp < 0:
		res.Message = fmt.Sprintf("Soulacy %s is newer than manifest version %s.", currentVersion, res.LatestVersion)
	default:
		res.Message = fmt.Sprintf("Soulacy %s is current.", currentVersion)
	}
	if res.UpdateAvailable && res.Artifact == nil {
		res.Message += fmt.Sprintf(" No %s/%s artifact was found in the manifest.", runtime.GOOS, runtime.GOARCH)
	}
	return res, nil
}

func resolveUpdateManifestSource(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("SOULACY_UPDATE_MANIFEST")); s != "" {
		return s
	}
	return strings.TrimSpace(viper.GetString("updates.manifest_url"))
}

func readUpdateManifest(ctx context.Context, source string) (updateManifest, error) {
	data, err := readUpdateManifestBytes(ctx, source)
	if err != nil {
		return updateManifest{}, err
	}
	var manifest updateManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return updateManifest{}, fmt.Errorf("update manifest: invalid JSON: %w", err)
	}
	if strings.TrimSpace(manifest.Product) != "" && !strings.EqualFold(manifest.Product, "soulacy") {
		return updateManifest{}, fmt.Errorf("update manifest: unexpected product %q", manifest.Product)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return updateManifest{}, fmt.Errorf("update manifest: version is required")
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
		resp, err := http.DefaultClient.Do(req)
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

type updateInstallOptions struct {
	ManifestSource string
	CurrentVersion string
	InstallDir     string
	DryRun         bool
	Yes            bool
}

func installUpdate(ctx context.Context, opts updateInstallOptions) (updateInstallResult, error) {
	check, err := checkForUpdate(ctx, opts.ManifestSource, opts.CurrentVersion)
	if err != nil {
		return updateInstallResult{}, err
	}
	res := updateInstallResult{updateCheckResult: check, DryRun: opts.DryRun}
	if !check.UpdateAvailable {
		res.Message = check.Message
		return res, nil
	}
	if check.Artifact == nil {
		return res, fmt.Errorf("update install: no %s/%s artifact in manifest", runtime.GOOS, runtime.GOARCH)
	}
	if !opts.DryRun && !opts.Yes {
		return res, fmt.Errorf("update install: pass --yes to replace local binaries, or --dry-run to verify only")
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
		return res, fmt.Errorf("update install: archive %s does not contain soulacy", check.Artifact.Name)
	}
	if _, ok := files["sy"]; !ok {
		return res, fmt.Errorf("update install: archive %s does not contain sy", check.Artifact.Name)
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

func readUpdateArtifact(ctx context.Context, manifestSource string, artifact updateArtifact) ([]byte, string, error) {
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
		resp, err := http.DefaultClient.Do(req)
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

func resolveUpdateArtifactSource(manifestSource string, artifact updateArtifact) (string, error) {
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
	if manifestSource == "" {
		return raw, nil
	}
	return filepath.Join(filepath.Dir(manifestSource), raw), nil
}

func verifyUpdateArtifact(artifact updateArtifact, data []byte) error {
	if artifact.Bytes > 0 && int64(len(data)) != artifact.Bytes {
		return fmt.Errorf("update artifact: byte size mismatch for %s: got %d want %d", artifact.Name, len(data), artifact.Bytes)
	}
	want := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if want == "" {
		return fmt.Errorf("update artifact: sha256 is required for %s", artifact.Name)
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

func artifactForCurrentPlatform(artifacts []updateArtifact) *updateArtifact {
	for i := range artifacts {
		if artifacts[i].OS == runtime.GOOS && artifacts[i].Arch == runtime.GOARCH {
			return &artifacts[i]
		}
	}
	return nil
}

func semverComparable(a, b string) bool {
	_, oka := semverParts(a)
	_, okb := semverParts(b)
	return oka && okb
}
