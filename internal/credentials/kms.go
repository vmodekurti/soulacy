package credentials

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// KMSProvider derives or retrieves the AES-256 encryption key for a given agentID.
type KMSProvider interface {
	// DeriveKey returns a 32-byte AES key for the given agentID.
	DeriveKey(ctx context.Context, agentID string) ([]byte, error)
}

// ---------------------------------------------------------------------------
// LocalKMS
// ---------------------------------------------------------------------------

// LocalKMS derives per-agent keys using HKDF-SHA256 keyed from a hardware-
// derived master secret. The master secret is resolved once at construction.
type LocalKMS struct {
	masterSecret []byte
}

// NewLocalKMS creates a LocalKMS. It attempts to derive the master secret from
// the platform hardware ID (IOPlatformUUID on macOS, /etc/machine-id on Linux).
// If neither is available a random ephemeral secret is used and a warning is
// written to stderr.
func NewLocalKMS() (*LocalKMS, error) {
	secret, err := platformSecret()
	if err != nil {
		// Fallback: random ephemeral secret. Log the warning via stderr
		// (zap not available here without an import cycle).
		fmt.Fprintln(os.Stderr, "soulacy/credentials: WARNING — could not derive hardware machine secret; using ephemeral random key. Credentials will not survive process restarts.")
		secret = make([]byte, 32)
		if _, rerr := io.ReadFull(rand.Reader, secret); rerr != nil {
			return nil, fmt.Errorf("credentials: generate fallback secret: %w", rerr)
		}
	}
	return &LocalKMS{masterSecret: secret}, nil
}

// DeriveKey returns a 32-byte AES-256 key for the given agentID via HKDF-SHA256.
func (k *LocalKMS) DeriveKey(_ context.Context, agentID string) ([]byte, error) {
	info := []byte("soulacy-credential-" + agentID)
	r := hkdf.New(sha256.New, k.masterSecret, nil, info)
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("credentials: hkdf derive: %w", err)
	}
	return key, nil
}

// platformSecret returns the hardware-bound machine identifier as raw bytes.
func platformSecret() ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return machinePlatformUUID()
	case "linux":
		return linuxMachineID()
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// machinePlatformUUID reads IOPlatformUUID from ioreg output on macOS.
var ioregUUIDRe = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)

func machinePlatformUUID() ([]byte, error) {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	matches := ioregUUIDRe.FindSubmatch(out)
	if len(matches) < 2 {
		return nil, fmt.Errorf("IOPlatformUUID not found in ioreg output")
	}
	return []byte(strings.TrimSpace(string(matches[1]))), nil
}

// linuxMachineID reads /etc/machine-id.
func linuxMachineID() ([]byte, error) {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return nil, fmt.Errorf("read /etc/machine-id: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return nil, fmt.Errorf("/etc/machine-id is empty")
	}
	return []byte(id), nil
}

// ---------------------------------------------------------------------------
// PassthroughKMS
// ---------------------------------------------------------------------------

// PassthroughKMS returns the same fixed 32-byte key for every agentID.
// Intended for testing or explicit key management scenarios.
type PassthroughKMS struct {
	key []byte
}

// NewPassthroughKMS creates a PassthroughKMS with the given 32-byte key.
// Returns an error if the key is not exactly 32 bytes.
func NewPassthroughKMS(key []byte) (*PassthroughKMS, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("credentials: passthrough KMS key must be 32 bytes, got %d", len(key))
	}
	k := make([]byte, 32)
	copy(k, key)
	return &PassthroughKMS{key: k}, nil
}

// DeriveKey returns the fixed key regardless of agentID.
func (p *PassthroughKMS) DeriveKey(_ context.Context, _ string) ([]byte, error) {
	out := make([]byte, 32)
	copy(out, p.key)
	return out, nil
}
