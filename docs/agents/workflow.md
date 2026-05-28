# Workflow DAGs

Workflows let you chain multiple agents into multi-step pipelines with typed inputs, outputs, and dependency resolution.

## Concepts

- **Step** — a single agent invocation within the workflow
- **DAG** — directed acyclic graph of steps; Soulacy resolves the execution order automatically
- **Template variables** — `{{input.*}}` and `{{steps.id.output}}` for passing data between steps

## Basic example

```yaml title="agents/report-writer.soul.yaml"
name: report-writer
description: Research a topic and produce a polished report
model: gpt-4o
system_prompt: Orchestrate the research and writing pipeline.
channels:
  - http

workflow:
  steps:
    - id: research
      agent: researcher
      input:
        query: "{{input.message}}"

    - id: outline
      agent: outliner
      input:
        research_notes: "{{steps.research.output}}"
      depends_on: [research]

    - id: write
      agent: writer
      input:
        outline: "{{steps.outline.output}}"
        research: "{{steps.research.output}}"
      depends_on: [outline]
```

When a user messages `report-writer`, the pipeline runs:

1. `researcher` runs with the user's message as `query`
2. `outliner` runs once `researcher` completes
3. `writer` runs once `outliner` completes, receiving both the outline and raw research

The final output is the result of the last step (`write`).

## Parallel steps

Steps without `depends_on` (or with the same dependencies) run in parallel:

```yaml
workflow:
  steps:
    - id: search_web
      agent: web-researcher
      input:
        query: "{{input.message}}"

    - id: search_docs
      agent: doc-searcher
      input:
        query: "{{input.message}}"

    # Both search_web and search_docs run in parallel.
    # synthesizer waits for both.
    - id: synthesize
      agent: synthesizer
      input:
        web_results: "{{steps.search_web.output}}"
        doc_results: "{{steps.search_docs.output}}"
      depends_on: [search_web, search_docs]
```

## Template variables

| Variable | Description |
|----------|-------------|
| `{{input.message}}` | The original user message |
| `{{input.session_id}}` | The session ID |
| `{{input.agent_id}}` | The workflow agent's ID |
| `{{steps.<id>.output}}` | The output of a completed step |

## Error handling

By default, if any step fails, the workflow fails and returns an error. Failed steps are pushed to the dead-letter queue for inspection.

```yaml
workflow:
  steps:
    - id: risky-step
      agent: external-api-caller
      input:
        url: "{{input.message}}"
      retry:
        max_attempts: 3
        backoff: exponential
        initial_delay: 1s
```

## Retry policy

```yaml
retry:
  max_attempts: 3          # default: 1
  backoff: exponential     # linear | exponential | fixed
  initial_delay: 500ms
  max_delay: 30s
```

## Step timeout

```yaml
- id: slow-step
  agent: data-processor
  input:
    data: "{{input.message}}"
  timeout: 60s
```

## Viewing workflow runs

Workflow execution produces OTEL traces — each step appears as a child span. View in Jaeger or any OTLP-compatible backend.
