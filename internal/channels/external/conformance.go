package external

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// RunConformance exercises a sidecar command against the External Channel
// Protocol v1 contract: hello within the deadline, valid negotiation,
// tolerance of unknown frames, and clean shutdown. Returns nil when the
// sidecar conforms; a descriptive error otherwise.
//
// This is the seed of the official conformance kit (story E11). Sidecar
// authors can run it via a tiny Go program or `soulacy` subcommand later;
// the adapter tests use it against the reference implementations.
func RunConformance(ctx context.Context, command string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("conformance: stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("conformance: stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("conformance: start: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	frames := make(chan Frame, 32)
	scanErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 4096), 1024*1024)
		for sc.Scan() {
			if f, err := ParseFrame(sc.Bytes()); err == nil {
				frames <- f
			}
			// Malformed lines are skipped; strict mode could flag them.
		}
		scanErr <- sc.Err()
		close(frames)
	}()

	// 1. hello within 5 seconds (unknown frames before it are fine).
	var hello Frame
	helloDeadline := time.After(5 * time.Second)
waitHello:
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return fmt.Errorf("conformance: sidecar exited before sending hello")
			}
			if f.Type == "hello" {
				hello = f
				break waitHello
			}
		case <-helloDeadline:
			return fmt.Errorf("conformance: no hello frame within 5s")
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// 2. Negotiation must succeed.
	v, err := Negotiate(hello)
	if err != nil {
		return fmt.Errorf("conformance: %w", err)
	}
	if err := WriteFrame(stdin, Frame{Type: "hello_ack", Protocol: v}); err != nil {
		return fmt.Errorf("conformance: write hello_ack: %w", err)
	}

	// 3. Sidecar must tolerate unknown frame types from the gateway.
	if err := WriteFrame(stdin, Frame{Type: "conformance.probe.v99"}); err != nil {
		return fmt.Errorf("conformance: write unknown frame: %w", err)
	}

	// 4. A send frame must not crash it (delivery isn't verifiable here).
	if err := WriteFrame(stdin, Frame{Type: "send", To: "conformance-chat", Text: "ping"}); err != nil {
		return fmt.Errorf("conformance: write send: %w", err)
	}

	// Give it a beat to crash if it's going to.
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return fmt.Errorf("conformance: sidecar exited after send/unknown frames")
	}

	// 5. shutdown must terminate the process within 5 seconds.
	if err := WriteFrame(stdin, Frame{Type: "shutdown"}); err != nil {
		return fmt.Errorf("conformance: write shutdown: %w", err)
	}
	_ = stdin.Close()
	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	select {
	case <-exitCh:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("conformance: sidecar did not exit within 5s of shutdown")
	}
}
