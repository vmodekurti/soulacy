# Skills

Soulacy supports Agent Skills: directories that contain a `SKILL.md` file with
frontmatter plus markdown instructions. Skills are not YAML fragments merged
into an agent. Instead, Soulacy exposes a lightweight skill catalog to the model
and lets the model call `read_skill` or `read_skill_file` when it needs the full
instructions or supporting files.

## Skill Layout

```text
skills/
  csv-analysis/
    SKILL.md
    scripts/
      analyze.py
```

Example `SKILL.md`:

```markdown
---
name: csv-analysis
description: Analyze CSV files and produce compact summaries.
---

Use this skill when the task involves CSV inspection, cleaning, aggregation, or
basic chart-ready summaries.
```

## Enabling Skills on an Agent

Add skill names to `SOUL.yaml`:

```yaml title="agents/analyst/SOUL.yaml"
id: analyst
name: Analyst
trigger: channel
channels: [http]
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: You help analyze local files.
skills:
  - csv-analysis
enabled: true
```

Use `skills: ["*"]` or `skills: ["all"]` to expose all installed skills.

When `skills` is empty or absent, the skill catalog and skill-reading built-ins
are not injected. This keeps small agents focused and reduces context cost.

## Skill Tools

Agents with skills can receive these built-ins, subject to the agent's
`builtins` filter:

| Tool | Purpose |
|------|---------|
| `read_skill` | Loads the full `SKILL.md` body for an enabled skill. |
| `read_skill_file` | Reads a file inside an enabled skill directory, such as `scripts/analyze.py`. |

## Skill Search Paths

The loader scans configured and conventional skill roots, including personal and
project-level directories such as:

- `~/.agents/skills`
- `~/.soulacy/skills`
- project-level skill directories configured for the gateway

Use the GUI or `config.yaml` to add additional skill directories when needed.

## Best Practices

- Keep the frontmatter `description` specific; it is what the model sees before
  deciding whether to call `read_skill`.
- Put reusable scripts, templates, or examples beside `SKILL.md`.
- Prefer skills for instructions and reusable procedure; prefer Python tools or
  MCP tools for executable capability.
- Enable only the skills an agent is likely to need.
