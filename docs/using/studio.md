# Studio — Intent-First Workflow Builder

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

## When a run fails: Debug in Studio

Any failed run in **Activity** has a **Debug in Studio** button. It loads the
exact failed run trace so Studio can see what really happened:

- which node failed, and its inputs and outputs;
- any missing variables or bad tool arguments;
- channel/delivery errors.

Studio then proposes a fix **in plain English** ("the `city` variable is never
set before the *Gather* step — bind it from the trigger message"). You can
**preview the SOUL.yaml / workflow diff** before saving anything.

### Build until it works

If you'd rather not iterate by hand, choose **Build until it works**. Studio runs
a *bounded* series of repair attempts, testing after each one, and stops as soon
as the workflow succeeds (or reports clearly if it can't converge). When a repair
succeeds, Studio captures the failing case as a **regression test** so the same
break can't silently return.

## See also

- [Workflow Templates](templates.md) — start from a vetted workflow instead of a
  blank Plan.
- [Evaluations](evaluations.md) — lock in behavior with `sy eval`.
- [Troubleshooting: Common failures](../troubleshooting/common-failures.md).
