# Python tool sandbox

Soulacy can run user-supplied Python tools under host-enforced **resource
limits** (`internal/sandbox`). This page describes precisely what that sandbox
does and — just as importantly — what it does **not** do, so operators do not
over-trust it.

> **One-line summary:** the sandbox is a *resource-exhaustion* guard, not a
> security boundary. It caps CPU, memory, file descriptors, and single-file
> write size. It does **not** isolate the filesystem, network, processes, or
> credentials. Treat every Python tool as code running with the **full
> privileges of the gateway process**.

## What the sandbox DOES

When `runtime.sandbox.enabled: true`, each Python tool subprocess is launched
through a hidden re-exec of the soulacy binary (`__exec-sandbox`) that applies
POSIX `setrlimit(2)` caps before `execve`-ing the real command:

| Limit        | rlimit          | Default | Effect                                                            |
| ------------ | --------------- | ------- | ----------------------------------------------------------------- |
| `cpu_seconds`| `RLIMIT_CPU`    | 30      | Kernel sends `SIGXCPU` then `SIGKILL` when CPU time is exhausted. |
| `memory_mb`  | `RLIMIT_AS`     | 512     | Caps the process's virtual address space.                         |
| `open_files` | `RLIMIT_NOFILE` | 256     | Caps the number of open file descriptors.                         |
| `file_size_mb`| `RLIMIT_FSIZE` | 64      | Caps the largest single file the process may write.               |

These limits stop a buggy or runaway tool from consuming unbounded CPU/RAM,
leaking file descriptors, or filling the disk via a single `open()`. That is
the entire scope of the protection.

It works as a single static binary on every Unix host — no external sandboxer,
container runtime, or kernel feature is required.

## What the sandbox does NOT do

The sandbox provides **no isolation** of any kind beyond the resource caps
above. In particular it does **NOT**:

- **Filesystem isolation** — the tool can read and write any path the gateway
  user can. There is no chroot, mount namespace, or read-only root. (Use
  `runtime.allowed_tool_dirs` to constrain *where tool scripts may live*, but
  that does not constrain what a running tool can touch.)
- **Network isolation** — the tool has the gateway's full network access. It
  can open arbitrary outbound connections. (SSRF protection on *built-in* HTTP
  tools is separate and does not cover arbitrary Python.)
- **Process / namespace isolation** — no PID, user, IPC, UTS, or network
  namespaces; no seccomp filter; no capability dropping. The tool runs as the
  same OS user with the same privileges as the gateway.
- **Credential isolation** — the tool inherits the gateway's environment,
  including any secrets present in it.

## Platform and reliability caveats

- **`RLIMIT_AS` is advisory on macOS.** Linux enforces the address-space cap
  strictly; macOS enforces it loosely — some `mmap`'d allocations can exceed
  the limit before the kernel notices. The cap still discourages large
  allocations but must not be relied on as a hard memory ceiling on macOS.
- **`setrlimit` failure is non-fatal.** If applying a limit fails (for example
  an edge case on Darwin), the sandbox logs a warning to stderr and **runs the
  tool anyway** rather than aborting it
  (`internal/sandbox/sandbox.go`, `RunSandboxedAndExit`). A tool may therefore
  run with fewer limits than configured, or none.
- **Windows / non-Unix hosts:** the wrapper is a no-op passthrough — no limits
  are applied at all.

## Recommendation

Only install Python tools you trust. If you need real isolation (untrusted
tools, multi-tenant deployments), run the gateway itself inside a container,
VM, or other OS-level sandbox with the filesystem, network, and privilege
boundaries you require. The built-in sandbox is a complement to such measures,
not a replacement.
