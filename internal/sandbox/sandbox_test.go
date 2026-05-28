package sandbox

import (
	"reflect"
	"testing"
)

// TestWrap_DisabledPassthrough — when limits are off, the wrapper must
// hand the engine back the exact command the engine asked to run. No
// extra args. No env mutation. The whole point of the Enabled switch is
// to be a true no-op so an operator can flip sandboxing off without
// touching the engine path.
func TestWrap_DisabledPassthrough(t *testing.T) {
	in := []string{"python3", "-c", "print('hi')"}
	out := Wrap("/usr/local/bin/soulacy", Limits{Enabled: false}, in)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("disabled wrap should be identity; got %v", out)
	}
}

// TestWrap_NoSelfPathPassthrough — discovering os.Executable() can fail
// in obscure setups. Rather than refuse to run the tool, the engine
// degrades to direct exec when self is empty. Verifying that here so a
// later refactor doesn't accidentally make the engine crash on missing
// self.
func TestWrap_NoSelfPathPassthrough(t *testing.T) {
	in := []string{"python3", "-c", "print('hi')"}
	out := Wrap("", DefaultLimits(), in)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("missing self should be identity; got %v", out)
	}
}

// TestWrap_BuildsCorrectArgv — golden case. When enabled, the wrapper
// emits [self, sentinel, --cpu=X, --mem=Y, --nofile=Z, --fsize=W, --,
// cmd…]. Order of the limit flags doesn't matter to the parser, but the
// `--` separator MUST precede the wrapped command or argv parsing will
// eat the wrong tokens.
func TestWrap_BuildsCorrectArgv(t *testing.T) {
	in := []string{"python3", "-c", "print('hi')"}
	out := Wrap("/usr/local/bin/soulacy", Limits{
		Enabled: true, CPUSeconds: 30, MemoryMB: 512, OpenFiles: 256, FileSizeMB: 64,
	}, in)
	want := []string{
		"/usr/local/bin/soulacy", "__exec-sandbox",
		"--cpu=30", "--mem=512", "--nofile=256", "--fsize=64",
		"--", "python3", "-c", "print('hi')",
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("argv mismatch\n got  %v\n want %v", out, want)
	}
}

// TestParseSandboxArgs_Roundtrip — feeding the output of Wrap back into
// parseSandboxArgs should produce the same Limits + the same trailing
// command. Without this, a typo in either side (flag name change, wrong
// `--` placement) silently breaks the sandbox at runtime.
func TestParseSandboxArgs_Roundtrip(t *testing.T) {
	in := []string{"python3", "-c", "x=1"}
	limitsIn := Limits{Enabled: true, CPUSeconds: 10, MemoryMB: 64, OpenFiles: 32, FileSizeMB: 4}
	argv := Wrap("/soulacy", limitsIn, in)

	limitsOut, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if limitsOut.CPUSeconds != limitsIn.CPUSeconds ||
		limitsOut.MemoryMB != limitsIn.MemoryMB ||
		limitsOut.OpenFiles != limitsIn.OpenFiles ||
		limitsOut.FileSizeMB != limitsIn.FileSizeMB {
		t.Errorf("limits roundtrip: got %+v want %+v", limitsOut, limitsIn)
	}
	if !reflect.DeepEqual(cmd, in) {
		t.Errorf("cmd roundtrip: got %v want %v", cmd, in)
	}
}

// TestParseSandboxArgs_MissingCommand — defensively: forgetting the
// `--` separator OR forgetting the trailing command should be reported,
// not silently treated as an empty command (which would execve to "").
func TestParseSandboxArgs_MissingCommand(t *testing.T) {
	cases := [][]string{
		{"soulacy", "__exec-sandbox", "--cpu=10"},          // no -- at all
		{"soulacy", "__exec-sandbox", "--cpu=10", "--"},    // -- but nothing after
	}
	for _, argv := range cases {
		_, _, err := parseSandboxArgs(argv)
		if err == nil {
			t.Errorf("expected error for argv=%v", argv)
		}
	}
}

// TestIsSandboxInvocation guards the entry-point check in main(). If we
// rename the sentinel, the check moves with it (it reads `sentinel` from
// this package), but a typo in `os.Args[1] == "…"` somewhere else would
// only surface as "sandbox limits never apply" — silent and bad. This
// test plus the Wrap test together ensure the round-trip works.
func TestIsSandboxInvocation(t *testing.T) {
	if !IsSandboxInvocation([]string{"soulacy", "__exec-sandbox", "--cpu=1", "--", "python"}) {
		t.Errorf("should detect sentinel")
	}
	if IsSandboxInvocation([]string{"soulacy", "serve"}) {
		t.Errorf("should not detect on normal invocation")
	}
	if IsSandboxInvocation([]string{"soulacy"}) {
		t.Errorf("should not detect on bare argv")
	}
}
