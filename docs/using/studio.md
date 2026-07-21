# Studio — Intent-First Workflow Builder

![Studio Visual Workflow Diagram](../assets/screenshots/studio_workflow.png)

Studio turns a plain-English description of the automation you want into a
runnable Soulacy workflow. You describe the outcome; Studio drafts a step plan,
generates the graph, checks it end-to-end, and — when a run fails — proposes a
fix you can review before saving.


You never have to start by dragging boxes on a canvas. The graph is *generated
from your intent*, not the other way around.

## The three views

Every workflow can be inspected three ways, and you can switch between them at
any time:

| View | What it shows | Use it to |
| --- | --- | --- |
| **Plan** | The workflow as plain-English steps, grouped into lanes | Understand and edit *what* happens |
| **Canvas** | The generated node graph | See data flow and wiring |
| **SOUL.yaml** | The underlying agent definition | Review or hand-edit the source of truth |

The Plan is the primary surface. Canvas and SOUL.yaml are always kept in sync
with it.

## Plan lanes

The Plan organizes steps into six lanes that map to how an automation actually
runs:

1. **Trigger** — what starts the workflow (a message, a schedule, a webhook).
2. **Gather** — collecting inputs and context (fetch a URL, read the knowledge
   base, pull a file).
3. **Think** — reasoning, analysis, or an LLM/agent step that decides what to do.
4. **Act** — taking an action (call a tool, run Python, hit an API).
5. **Verify** — checking the result is sane before anyone sees it.
6. **Deliver** — sending the result to a channel or returning it in Chat.

Entry and exit are **implicit** — you won't see confusing "start"/"end" blocks
to wire up. Studio adds them for you.

## Building a workflow

1. Open **Studio** and describe the automation in one or two sentences, e.g.
   *"Every morning, summarize my unread email and post the summary to Slack."*
2. Studio drafts a **Plan**. Read it top to bottom — it should match your intent.
3. Add or change steps in natural language ("also attach the original links",
   "only include emails from my team"). Studio recommends the right block type —
   a Python step, a tool call, or a sub-agent — for each addition.
4. Switch to **Canvas** to see the generated graph, or **SOUL.yaml** to read the
   source.
5. Save. Every workflow passes a **whole-workflow integrity check** before it can
   be saved — dangling references, missing variables, unroutable outputs, and
   invalid Python are caught here, not at runtime.

### Streamed vs Wizard generation

The **Generate** button has a Streamed / Wizard split — Streamed (the default)
runs all five pipeline phases (`clarify_intent → choose_strategy →
build_graph → validate → repair`) in one shot and streams a live transcript
below the canvas so you can see each LLM I/O as it lands. Wizard mode opens
the refinement modal with a **Wizard-steps breadcrumb** at the top, letting
you inspect and edit intermediate output between phases. A `(once)` label
appears on the split-button when you're overriding the persisted default; the
default itself lives under **Studio model modal → Generate presentation**.

### Runtime intent presets

The Studio model modal has a **Runtime intent** section with three named
presets:

- **Fast local** — small local model, tight timeouts, high loop cap. Best for
  quick "does the shape look right?" iteration.
- **Reliable local** — patient timeouts sized for weaker local models, so a
  slow-first-token model doesn't get killed mid-turn. The default.
- **Cloud quality** — long total budget with generous per-step timeouts,
  applied even for cloud providers so long plans have room to breathe.

Selecting an intent persists to `llm.studio.preset`; the next Save bakes those
timeouts into any agent Definition that doesn't already override them.

### Reasoning-agent contract checks

Studio's contract validator runs a full battery of checks on **reasoning
agents** (agents authored as a system prompt + tool allowlist rather than a
graph). Each check produces a blocker (Save-blocking) or warning:

- `agent.system_prompt` — blocks an empty prompt (the prompt IS the agent
  spec); warns under ~40 words.
- `agent.tool_allowlist` — blocks a ReAct loop with no tools / peers / skills
  / knowledge bases; warns on the same for non-ReAct.
- `agent.peer_graph` — flags dangling `agent__<id>` mentions in the system
  prompt.
- `agent.prompt_hygiene` — warns when the prompt says "use \`X\`" for a tool
  that isn't in the allowlist.
- `agent.step_budget` — realism band on MaxTurns / StepTimeout / TotalTimeout
  / RunTimeout; blocks unbounded ReAct > 40 turns; warns when
  `total_timeout < max_turns × step_timeout`.
- `agent.channel_delivery` — warns when `channel.send` is in Tools but
  Channels is empty.
- `agent.llm_fit` — blocks embedding models; warns on weak-JSON models,
  small-context + high-turn combos, and provider-not-in-allowed_providers.
- `agent.capability_scope` — warns on privileged scheduled non-Unattended
  agents, and on very open policies.
- `agent.persona_consistency` — flags contradictions like `MustNot` +
  `tool_choice=required`, or JSON-format constraints without
  `response_format`.
- `agent.builtin_scope` — blocks opt-out-of-everything shapes; warns on
  `kb_search` without Knowledge or `read_skill` without Skills.

The Save-blocking capability audit runs on top of these: any save that would
escalate an agent's tier (ReadOnly → Active → Privileged) while it has
interactive channel bindings pops a blocking modal listing the tier diff,
warnings, and affected bindings, and requires an explicit acknowledgement
before the save proceeds.

## When a run fails: Debug in Studio

Any failed run in **Activity** has a **Debug in Studio** button. It loads the
exact failed run trace so Studio can see what really happened:

- which node failed, and its inputs and outputs;
- any missing variables or bad tool arguments;
- channel/delivery errors.

Studio then proposes a fix **in plain English** ("the `city` variable is never
set before the *Gather* step — bind it from the trigger message"). Three
things happen when you click Debug in Studio:

1. **The failing input is pre-filled** into the test bench so the fix can be
   verified against the exact input that broke the run — no reconstructing.
2. **The structured run trace loads** into the runTrace panel and the
   run-history picker, alongside the compacted evidence blob inside the heal
   result — so you're not restricted to the summary.
3. **The heal result routes through a diff panel** with **Apply this fix** /
   **Cancel** buttons. The canvas only changes when you confirm; the proposed
   diff is visible before it becomes reality.

### Build until it works

If you'd rather not iterate by hand, choose **Build until it works**. Studio runs
a *bounded* series of repair attempts, testing after each one, and stops as soon
as the workflow succeeds (or reports clearly if it can't converge). When a repair
succeeds, Studio captures the failing case as a **regression test** so the same
break can't silently return.

The Build report has two sections above the attempt log so you can tell
"needs my input" from "still trying":

- **Needs your input** — external blockers Studio can't fix on its own
  (missing credential, bot not invited, rate limit, invalid destination). The
  report header prefers this over a raw residual dump — "Stopped — needs
  your input: …" rather than "Could not fully fix it automatically".
- **What Studio changed** — a plain-language rollup, one bullet per changed
  attempt, of the edits the loop applied. Makes it obvious what actually
  moved between attempts.

## See also

- [Workflow Templates](templates.md) — start from a vetted workflow instead of a
  blank Plan.
- [Evaluations](evaluations.md) — lock in behavior with `sy eval`.
- [Troubleshooting: Common failures](../troubleshooting/common-failures.md).
