# Workflow Steps

`workflow` turns an agent into a checkpointed sequence of tool calls. When an
agent declares `workflow:`, the runtime delegates to the workflow executor
instead of the free-form LLM loop.

Current workflows are sequential tool pipelines with checkpoint persistence.
They are useful for repeatable jobs where you want restart/resume behavior and
clear step status.

## Basic Example

```yaml title="agents/report-writer/SOUL.yaml"
id: report-writer
name: Report Writer
description: Run a repeatable reporting pipeline
trigger: channel
channels: [http]
llm:
  provider: openai
  model: gpt-4o
system_prompt: Run the reporting workflow.

workflow:
  steps:
    - id: search
      tool: web_search
      input: '{"query":"{{.trigger}}"}'
      output: search_results
      on_error: retry

    - id: summarize
      tool: summarize_report
      input: '{"search_results":{{.search_results}}}'
      output: report
      on_error: abort

enabled: true
```

## Step Fields

| Field | Description |
|-------|-------------|
| `id` | Unique step ID within the workflow. Used as the checkpoint key. |
| `tool` | Tool name to invoke. |
| `prompt` | Optional prompt text for future LLM-assisted step behavior. |
| `if` | Go template condition. Empty, `false`, or `0` skips the step. |
| `on_error` | `abort` (default), `retry`, or `skip`. |
| `input` | Go template that renders the tool input string. |
| `output` | Variable name used to store this step's parsed JSON result. |

## Template Variables

The workflow starts with:

| Variable | Description |
|----------|-------------|
| `.trigger` | Flattened inbound message text/parts. |

When a step has `output: some_name`, its result becomes available as
`.some_name` to later steps.

Example:

```yaml
workflow:
  steps:
    - id: gather
      tool: web_search
      input: '{"query":"{{.trigger}}"}'
      output: search

    - id: write
      tool: write_brief
      input: '{"topic":"{{.trigger}}","search":{{.search}}}'
      output: brief
```

## Error Handling

`on_error` controls step failure:

| Value | Behavior |
|-------|----------|
| `abort` or empty | Mark the step failed and stop the workflow. |
| `retry` | Retry the tool once, then abort if it still fails. |
| `skip` | Mark the step completed with no state and continue. |

## Checkpoints

After each step, Soulacy stores the step status and output. If a workflow run is
resumed with the same run ID, completed steps are skipped and their saved output
is restored into the template variable map.

## When To Use Workflows

Use workflows for deterministic operational jobs:

- scheduled reports,
- ingest-and-summarize tasks,
- multi-step sync jobs,
- pipelines where retry/skip behavior matters.

Use peer agents and normal tool calling when the model should decide the plan at
runtime.
