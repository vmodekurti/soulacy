# Installing Skills

Install Agent Skills from a local directory, a registry slug, or a git source — every remote install is checksummed, security-scanned, and consented before it touches your agents.

## Quick start

```bash
# Local directory (copied into ~/.soulacy/skills/)
sy skill install ./my-skill

# Registry slug (resolved through your configured registries)
sy skill install self-improving-agent

# Git source (works out of the box, no registry config needed)
sy skill install github.com/user/my-skill
```

A skill package is a directory with a `SKILL.md` at its root. After install,
list and inspect skills with:

```bash
sy skill list
sy skill get <name>
```

## Local directory installs

`sy skill install ./my-skill` validates that the directory contains a
`SKILL.md` and copies it into `~/.soulacy/skills/<name>/`. Nothing else
happens — placing files on your own disk is your call. You can equally just
copy a skill directory into `~/.soulacy/skills/` by hand.

## Remote installs: the full flow

When the argument is *not* a local directory, it is treated as a package slug
and resolved through the `registries:` block in `config.yaml` (see
[Skill Sources](skill-sources.md) and [Package Registries](registries.md)).
With no registries configured, a bare git provider is used as fallback, so
`sy skill install github.com/user/my-skill` always works.

Every remote install walks the same pipeline:

1. **Resolve** — registries are queried in priority order with fallback. The
   CLI prints what it found: `Resolved <slug>@<version> via registry "<id>"`,
   the description, the archive sha256, and the signature provenance:

    - `signature: ed25519, verified against registry "<id>"'s signing_key during fetch`
    - `signature: present but UNVERIFIED — set signing_key on registry "<id>" to enforce verification`
    - `signature: none (unsigned package)`

2. **Staged fetch** — the package downloads into a temporary
   `.staging-…` directory under your skills root. Archives are
   sha256-verified before extraction; git sources derive integrity from the
   clone. A package without `SKILL.md` at its root is rejected here. The
   staging directory never survives a failed or aborted install.

3. **Security report** — the [safety introspection pipeline](safety.md) runs
   over the staged files and prints its verdict and findings:

    ```text
    Safety introspection: ✓ passed safety checks
    ```

    or, for example:

    ```text
    Safety introspection: ✗ DANGER — critical findings
      CRITICAL (static) [tool.py:3]: dangerous call: eval() …
    ```

4. **Consent prompt** — the CLI summarises the package (skill heading, tool
   libraries, declared schema migrations, requested capabilities, requested
   credentials) and asks:

    ```text
    Install <slug>@<version>? [y/N]
    ```

5. **Activate & hot-load** — on consent the staging directory moves to
   `~/.soulacy/skills/<name>` and the CLI calls the gateway's
   `POST /api/v1/skills/rescan` so the skill is live immediately. If the
   gateway is unreachable, the skill loads on the next gateway restart.

If a skill with the same name is already installed, the install aborts with
a message telling you to remove the existing directory first.

## Non-interactive installs: `--yes`

```bash
sy skill install github.com/user/my-skill --yes
```

`--yes` (or `-y`) skips the consent prompt for `pass` and `caution` verdicts.

!!! warning "`--yes` never bypasses a danger verdict"
    When the safety report's verdict is **danger**, the CLI always demands an
    interactive confirmation:

    ```text
    ⚠ CRITICAL findings — explicit confirmation required (--yes does not apply).
    ```

    In a non-interactive context (CI, scripts) a danger verdict therefore
    always aborts the install. This is by design — review the findings
    yourself before installing anything flagged critical.

## Installing from the GUI

The **Skills** page in the web GUI offers:

- **⚡ From AgenticSkills** — paste an `agenticskills.io` skill URL; the
  `SKILL.md` is downloaded and hot-loaded with no restart.
- **➕ Skill sources** — review and add registries (skill directories like
  skills.sh, Soulacy registries, git hosts) so slugs resolve for installs.
  See [Skill Sources](skill-sources.md).

Slug-based GUI installs go through the same registry engine and the same
safety pipeline as the CLI — the security report and permission grants are
shown in the approval dialog before anything activates.

## Where skills live

| Path | Purpose |
|---|---|
| `~/.soulacy/skills/<name>/` | install root used by `sy skill install` |
| `SKILL.md` | required at the package root — frontmatter + instructions |

Skills are exposed to agents as an `available_skills` catalog; agents call
`read_skill` to load the full instructions on demand. See
[Skills](../agents/skills.md) for enabling skills per agent.

## Troubleshooting

- **`"<arg>" is not a local directory and no configured registry resolves it`**
  — the slug was not found by any registry. Check `sy registry list`, or use
  an addressed git source (`github.com/user/repo`).
- **`package "<slug>" has no SKILL.md at its root`** — the source is not a
  skill package. Plugins install through the Plugins GUI page instead; see
  [Plugins](plugins.md).
- **`Gateway rescan failed … the skill loads on the next gateway restart`** —
  the install succeeded; only the hot-load was skipped because the gateway
  was unreachable.
