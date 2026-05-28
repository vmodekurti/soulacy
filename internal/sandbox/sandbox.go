// Package sandbox runs untrusted subprocesses (currently: user-supplied
// Python tools) under host-enforced resource limits.
//
// PRODUCTION_AUDIT — F1 (2026-05-27): before this package, the engine
// fork+exec'd python -c <script> directly. A buggy tool could consume
// unbounded CPU, RAM, or file descriptors — same OS permissions as the
// gateway process. The sandbox closes the obvious holes via syscall-level
// rlimits enforced inside a hidden subcommand of the soulacy binary
// itself ("__exec-sandbox"), which sets the limits and then execve's the
// real command. No external sandboxer required — works as a single
// static binary on every Unix host.
//
// On non-Unix platforms (currently: Windows), the wrapper is a no-op
// passthrough. The build does NOT depend on this package at the platform
// level — the public API is OS-agnostic; the syscall details live in
// wrap_unix.go / wrap_other.go under build tags.
package sandbox

import (
	"fmt"
	"os"
	"strconv"
)

// Limits captures the per-subprocess resource caps. Zero values mean
// "no limit applied for that knob" — useful so a partial config doesn't
// accidentally clamp something the operator didn't intend to.
type Limits struct {
	// CPUSeconds caps wall-equivalent CPU usage. The kernel sends SIGXCPU
	// at the soft limit and SIGKILL at the hard limit (we set them equal).
	CPUSeconds int

	// MemoryMB caps virtual address space (RLIMIT_AS on Linux; advisory
	// on macOS, where the kernel doesn't strictly enforce AS but the
	// limit still discourages large mmap'd allocations).
	MemoryMB int

	// OpenFiles caps RLIMIT_NOFILE — file descriptors per process. Tools
	// that leak FDs hit this before they exhaust the host.
	OpenFiles int

	// FileSizeMB caps RLIMIT_FSIZE — the largest single file the process
	// may write. Stops a runaway tool from filling the disk via one open().
	FileSizeMB int

	// Enabled is the master switch. When false, Wrap() returns the input
	// command unchanged so the rest of the pipeline doesn't have to
	// branch on whether sandboxing is on.
	Enabled bool
}

// DefaultLimits returns conservative caps suitable for typical agent
// tools (≤30s of CPU, ≤512 MiB RAM, ≤256 FDs, ≤64 MiB of file output).
// Operators can override individually via config.yaml.
func DefaultLimits() Limits {
	return Limits{
		Enabled:    true,
		CPUSeconds: 30,
		MemoryMB:   512,
		OpenFiles:  256,
		FileSizeMB: 64,
	}
}

// Wrap takes the command the engine wants to run (e.g. ["python3", "-c", "..."])
// and returns the command vector the engine should ACTUALLY exec — either
// the original (sandboxing disabled) or [self, "__exec-sandbox", flags…, "--", cmd…].
//
// `self` should be os.Executable() — passing it explicitly keeps the call
// pure-functional and trivially testable.
//
// The returned slice is always non-empty; the engine plugs argv[0] into
// exec.Command and the rest as args.
func Wrap(self string, l Limits, cmd []string) []string {
	if !l.Enabled || self == "" || len(cmd) == 0 {
		return cmd
	}
	out := make([]string, 0, len(cmd)+10)
	out = append(out, self, sentinel)
	if l.CPUSeconds > 0 {
		out = append(out, "--cpu="+strconv.Itoa(l.CPUSeconds))
	}
	if l.MemoryMB > 0 {
		out = append(out, "--mem="+strconv.Itoa(l.MemoryMB))
	}
	if l.OpenFiles > 0 {
		out = append(out, "--nofile="+strconv.Itoa(l.OpenFiles))
	}
	if l.FileSizeMB > 0 {
		out = append(out, "--fsize="+strconv.Itoa(l.FileSizeMB))
	}
	out = append(out, "--")
	out = append(out, cmd...)
	return out
}

// sentinel is the hidden subcommand. The leading "__" double-underscore
// signals "internal use only" and prevents accidental collision with
// future user-facing subcommands.
const sentinel = "__exec-sandbox"

// IsSandboxInvocation returns true when argv looks like a sandbox
// re-exec. Call from main() BEFORE flag parsing so the wrapped command
// runs without paying for the gateway's normal startup costs.
func IsSandboxInvocation(argv []string) bool {
	return len(argv) >= 2 && argv[1] == sentinel
}

// RunSandboxedAndExit is the entry point invoked from main() when the
// process is a sandbox wrapper. Parses limit flags, applies them, then
// execve's the requested command. Never returns on success — on failure,
// writes the error to stderr and exits non-zero.
//
// argv is os.Args; we slice past argv[0] (binary path) and argv[1]
// (the sentinel) to find our own flags + `--` + the wrapped command.
func RunSandboxedAndExit(argv []string) {
	flags, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: %v\n", err)
		os.Exit(2)
	}
	if err := applyLimits(flags); err != nil {
		// Don't abort the user's tool on a setrlimit failure — log it
		// and continue. A failed AS limit on macOS shouldn't stop the
		// engine from running a perfectly normal tool.
		fmt.Fprintf(os.Stderr, "sandbox: warn: %v\n", err)
	}
	if err := execCommand(cmd); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: exec %q: %v\n", cmd[0], err)
		os.Exit(127)
	}
}

func parseSandboxArgs(argv []string) (Limits, []string, error) {
	var l Limits
	l.Enabled = true
	i := 2 // skip argv[0] (binary) + argv[1] (sentinel)
	for ; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			i++
			break
		}
		switch {
		case hasPrefix(a, "--cpu="):
			v, err := strconv.Atoi(a[len("--cpu="):])
			if err != nil {
				return l, nil, fmt.Errorf("bad --cpu: %w", err)
			}
			l.CPUSeconds = v
		case hasPrefix(a, "--mem="):
			v, err := strconv.Atoi(a[len("--mem="):])
			if err != nil {
				return l, nil, fmt.Errorf("bad --mem: %w", err)
			}
			l.MemoryMB = v
		case hasPrefix(a, "--nofile="):
			v, err := strconv.Atoi(a[len("--nofile="):])
			if err != nil {
				return l, nil, fmt.Errorf("bad --nofile: %w", err)
			}
			l.OpenFiles = v
		case hasPrefix(a, "--fsize="):
			v, err := strconv.Atoi(a[len("--fsize="):])
			if err != nil {
				return l, nil, fmt.Errorf("bad --fsize: %w", err)
			}
			l.FileSizeMB = v
		default:
			return l, nil, fmt.Errorf("unknown flag %q", a)
		}
	}
	if i >= len(argv) {
		return l, nil, fmt.Errorf("missing command after --")
	}
	return l, argv[i:], nil
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}
