# Declarative Cyclic Flow Graphs — Story E25

Workflows gained a graph form: `workflow.nodes` + `workflow.edges` in
SOUL.yaml. Unlike linear `steps`, graphs support conditional routing and
BOUNDED cycles (refine→judge loops, retry-until-pass, escalation paths),
compiled onto the existing checkpointing executor so resume-after-crash
keeps working.

## SOUL.yaml shape

```yaml
workflow:
  entry: refine                # default: first node
  max_node_executions: 50      # global safety budget (default 100)
  nodes:
    - id: refine
      tool: improve_draft      # kind inferred: tool
      input: '{"draft": {{.verdict.feedback | printf "%q"}} }'
    - id: judge
      tool: evaluate_draft
      output: verdict          # flow var holding this node's JSON result
    - id: notify
      agent: editor-agent      # kind=agent → invoked as agent__editor-agent
    - id: fork                 # neither tool nor agent → branch (no action)
  edges:
    - {from: refine, to: judge, max_iterations: 10}
    - {from: judge, to: refine, if: '{{not .verdict.ok}}', max_iterations: 5}
    - {from: judge, to: notify}        # fallback (declaration order matters)
    - {from: notify, to: end}          # "end"/absent target terminates
```

Semantics:

- **Nodes** run a tool (`tool:`), a peer agent (`agent:` → `agent__<id>`),
  or nothing (`branch` — pure routing). `input` is a Go template over the
  flow vars (`trigger` + every node's `output`); `on_error` is
  abort (default) | skip | retry.
- **Edges** from a node are evaluated in declaration order; the first one
  whose `if` predicate renders truthy AND whose traversal budget remains
  is taken. No eligible edge → the flow ends with the last node's result.
- **Bounded cycles**: every edge has `max_iterations` (default **1**), so
  cycles terminate by construction — a back edge must explicitly raise
  its budget. `max_node_executions` (default 100) backstops the whole run.
- **Checkpoints & resume**: each node visit checkpoints under
  `<node>#<visit>` in the existing store. Re-running a crashed run ID
  restores completed visits (vars included) and recomputes the same
  deterministic path — only unfinished work executes.
- Tool outputs that are JSON documents are unwrapped so predicates can
  address fields (`{{.verdict.ok}}`); plain text stays a string.

## Two entry points, one walker

- **Workflow runs** (cron/schedule): `WorkflowExecutor.Run` detects
  `nodes` and walks the graph with checkpoint hooks (internal/runtime/flow.go).
- **Chat runs**: `reasoning.strategy: flow` routes messages through the
  same graph via the E15 strategy registry (`sdk/reasoning.Config.Flow`);
  node actions go through the engine's standard tool-policy bridge.
  Steps surface as the usual reasoning.step events.

Compile-time validation (duplicate/missing node ids, unknown edge
endpoints, bad kinds, unknown entry) refuses the graph with precise
errors. Contract types live in `sdk/reasoning/flow.go`; the walker in
`internal/reasoning/flow.go` (`CompileFlow`, `RunFlow`, `FlowHooks`).

## GUI

The Flow page renders graph-form agents read-only: BFS-column layout,
entry wired from the trigger, predicate + `↺×N` budget labels on edges,
cycle back-edges highlighted amber, terminal edges into Output. Editing
arrives in a later story.
