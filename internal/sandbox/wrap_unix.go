//go:build linux || darwin

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// applyLimits sets every requested rlimit before execve. Each failure is
// returned as the first error (the caller logs and continues — a Darwin
// host that ignores RLIMIT_AS in some edge case shouldn't block the
// actual command from running).
func applyLimits(l Limits) error {
	if l.CPUSeconds > 0 {
		if err := setLim(unix.RLIMIT_CPU, uint64(l.CPUSeconds)); err != nil {
			return fmt.Errorf("rlimit cpu: %w", err)
		}
	}
	if l.MemoryMB > 0 {
		// RLIMIT_AS caps virtual address space. Linux enforces it
		// strictly; macOS enforces it loosely (some mmap'd allocations
		// can exceed it before the kernel notices). Still worth setting
		// — even loose enforcement discourages runaway allocations.
		if err := setLim(unix.RLIMIT_AS, uint64(l.MemoryMB)<<20); err != nil {
			return fmt.Errorf("rlimit mem: %w", err)
		}
	}
	if l.OpenFiles > 0 {
		if err := setLim(unix.RLIMIT_NOFILE, uint64(l.OpenFiles)); err != nil {
			return fmt.Errorf("rlimit nofile: %w", err)
		}
	}
	if l.FileSizeMB > 0 {
		if err := setLim(unix.RLIMIT_FSIZE, uint64(l.FileSizeMB)<<20); err != nil {
			return fmt.Errorf("rlimit fsize: %w", err)
		}
	}
	return nil
}

func setLim(which int, value uint64) error {
	rlim := unix.Rlimit{Cur: value, Max: value}
	return unix.Setrlimit(which, &rlim)
}

// execCommand replaces the current process with cmd[0] cmd[1:]. Inherits
// stdin/stdout/stderr/env from the parent (the engine has already wired
// them up). Never returns on success.
//
// IMPORTANT: execve(2) does NOT walk $PATH the way exec.Command does —
// it requires an absolute path. So when the engine asks for "python3"
// (no slash), we PATH-resolve it ourselves via exec.LookPath. Without
// this step F1's sandbox wrapper would turn every "python3"-as-name
// invocation into ENOENT — observed on 2026-05-28 as "Python interpreter
// could not be found" failures from agents whose pythonBin was just
// "python3" instead of "/opt/homebrew/bin/python3".
func execCommand(cmd []string) error {
	bin := cmd[0]
	if !strings.ContainsRune(bin, '/') {
		resolved, err := exec.LookPath(bin)
		if err != nil {
			return fmt.Errorf("look path %q: %w", bin, err)
		}
		bin = resolved
	}
	// syscall.Exec performs an execve; the child IS this process now.
	return syscall.Exec(bin, cmd, os.Environ())
}

// syscallEnviron is kept around for the rare case a future hardening
// wants to scrub the environment (PATH stripping, dropping AWS keys,
// etc.) without touching applyLimits/execCommand. Currently a no-op.
func syscallEnviron() []string { return os.Environ() }
