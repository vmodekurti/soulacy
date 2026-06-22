# Soulacy: Building AI Agents You Actually Own

### A local-first, model-agnostic platform for agents that do real work — not just chat.

---

Most "AI agents" today are a thin wrapper around someone else's model, running on someone else's servers, with your data passing through someone else's logs. They're impressive in a demo and frustrating in production. The moment you want one to *do* something — search the web, run a command, file a task, talk to an internal system, then come back tomorrow and do it again — the cracks show. The agent forgets. It can't reach your tools. It silently hands your context to a vendor. And when you ask "where does my data go, which model is this, and can I run it on my own machine?", the answer is usually "you can't."

Soulacy is a different bet. It's a self-hostable platform for building, running, and operating AI agents — one where *you* own the stack: your models, your data, your infrastructure, your guardrails. You can run it entirely on local models with nothing leaving your machine, or wire in frontier cloud models where you need the horsepower, and mix the two freely. It ships as a single gateway with a full web UI, a CLI, an API, and a visual builder — so a non-engineer can assemble an agent by describing it in plain English, and an engineer can define one in a YAML file and drive it from the command line.

This is a tour of what Soulacy is, the ideas behind it, and how the pieces fit together.

## The core idea: agents as first-class, declarative things

An agent in Soulacy isn't a prompt buried in a script. It's a declared, version-controllable definition — a small file that says who the agent is, what it can do, which model it runs on, how it reasons, what it remembers, and how the outside world reaches it. Here's the shape of one:

```yaml
name: Daily Briefing
system_prompt: |
  You are a concise morning-briefing assistant. Each weekday you gather the
  day's key news, summarize it, and post it to the team.
llm:
  provider: ollama
  model: llama3.3:70b
tools: [web_search]
reasoning:
  strategy: react
trigger:
  type: cron
  schedule:
    cron: "0 8 * * 1-5"
  output:
    channel: telegram
    to: "team-channel"
```

That single declaration is enough for the platform to do a lot: register the agent, give it the web-search tool, run it on a local model with an iterative reasoning loop, wake it every weekday at 8 a.m., and deliver its output to Telegram. No glue code. The definition *is* the agent.

This declarative core is what makes everything else composable. Because agents are data, you can build them by hand, generate them from a description, hand them to a teammate, diff them in git, or spin up a dozen specialized peers that delegate to one another.

## Bring your own models — and use the right one for each job

Soulacy is deliberately model-agnostic. It speaks to local runtimes like **Ollama** and to cloud providers — **OpenAI, Anthropic, Google Gemini, Groq, OpenRouter, NVIDIA NIM**, and any OpenAI-compatible endpoint. Switching providers is a config change, not a rewrite.

But "model-agnostic" undersells the more useful idea underneath: **different tasks need different models, and you shouldn't have to pick just one.** Soulacy treats model selection as a set of *roles* rather than one global default:

- a **chat** model for an agent's everyday conversation — where a cheap, fast, local model is often perfect;
- a **reasoner** model for the hard multi-step planning of an agentic loop, where reliability matters more than cost;
- a **studio** model for building and compiling new agents, which leans heavily on structured output;
- an **embedder** for turning documents into searchable vectors.

The practical payoff is real. You can run your agents day-to-day on a 70-billion-parameter model on your own Mac or server — private and free — while pointing the *reasoning* and *building* work at a frontier model only when the task demands it. The model that runs an agent doesn't have to be the model that *builds* it. That separation is the difference between "local models are a toy" and "local models do 80% of the work and I escalate the hard 20%."

## How an agent thinks: from a single call to a reasoning loop

Not every task needs the same amount of "thinking." Soulacy lets each agent choose its execution model:

- **Classic single-call** — the agent answers in one shot. Fast and cheap; ideal for straightforward chat or transformation.
- **ReAct** — the agent runs a *think → act → observe* loop: it reasons about the goal, calls a tool, reads the result, and adapts. This is what you want when the steps depend on intermediate results you can't know up front — for example, an agent that creates a record, gets back an ID, then uses that ID in the next call.
- **Plan-Execute** — the agent decomposes a long, multi-phase job into an explicit plan and then executes it step by step.

There's also a fourth mode that's almost the opposite of "thinking on the fly": a fixed **workflow**. Sometimes you don't want the agent improvising at all — you want the same deterministic steps, in the same order, every single run. For a nightly pipeline ("search, summarize, post"), a frozen graph is cheaper, more reviewable, and more reliable than a reasoning loop.

The honest tension here is that *most people don't know which mode their task needs* — and picking wrong is a quiet source of failure. A fixed workflow on an exploratory task is brittle; a reasoning loop on a rote pipeline is wasteful. So Soulacy's visual builder doesn't just generate something and walk away — it **assesses the task and recommends the execution model**, with a one-line rationale, and tells you how to switch. That brings us to the most distinctive part of the platform.

## Studio: describe it in English, get a working agent

Studio is Soulacy's visual builder, and it inverts the usual "drag fifty nodes onto a canvas" experience. You type what you want in plain language:

> *"Every weekday at 7 a.m., search for high-quality articles on AI research and security, pick the top ten, summarize them, and post the digest to Slack."*

…and the compiler turns that intent into a real, multi-node **flow graph** — tool nodes that fetch and act, agent nodes that reason and summarize, branch nodes that fork on conditions, and small inline Python nodes for the glue that no off-the-shelf tool covers. The graph appears on a canvas you can inspect, rewire, test, and save as an agent.

Crucially, the compiler is *grounded in your actual environment*. It knows which tools, skills, agents, and connected MCP servers you really have — including each tool's exact name and the precise arguments it accepts — so it wires up real capabilities instead of inventing plausible-sounding ones that fail at runtime. When your prompt references "my notebook tool" or "post to Linear," it matches that to the genuine tool and passes the right parameters.

And as mentioned, Studio is opinionated about *how* the agent should run. After it builds the graph, it surfaces a recommendation — *"This looks like a ReAct task, because the steps depend on values you won't know until earlier steps run"* — so you're nudged toward the execution model that will actually work, not just the one the builder happened to emit.

The result is a genuine bridge between no-code and code: a non-developer can stand up a working, scheduled, multi-step agent from a sentence, and a developer can open the resulting definition, refine it, and check it into version control.

## Giving agents hands: tools, MCP, skills, and plugins

A chatbot talks. An agent *acts*. Soulacy gives agents several layers of capability:

**Built-in tools** cover the common ground out of the box — web search, reading and writing files, fetching URLs, searching knowledge bases, and more. The web-search tool alone is provider-flexible, backing onto Ollama's hosted search, Tavily, or Serper.

**System tools** — running shell commands, executing scripts, installing libraries — exist for the agents that genuinely need to operate a machine. These are deliberately gated behind a double opt-in (the agent must hold a "system" capability *and* the server must allow it) and run inside a sandbox with resource limits, because handing an LLM a shell is powerful and demands care.

**MCP (Model Context Protocol) servers** are how Soulacy plugs into the broader, fast-growing ecosystem of external tools. Connect an MCP server — for GitHub, a database, a SaaS product, anything that speaks the protocol — and its tools become available to your agents under clean, namespaced names. A single server might expose dozens of tools, and the platform makes them first-class citizens the agent (and the Studio compiler) can reason about and call correctly.

**Skills** are reusable packets of instruction — a focused "how to do X well" that an agent loads on demand when a task matches. They're installable from a local directory, a registry slug, or a Git repository, so good agent behavior becomes shareable infrastructure rather than copy-pasted prompt text.

**Plugins** bundle all of the above — tools, skills, channels, UI — into installable units, so a whole capability can be dropped into a deployment at once.

The throughline: instead of one monolithic "do everything" prompt, capability is modular, declared, and reusable. Agents are assembled from real, inspectable parts.

## The Workboard: agents and humans, working the same board

Autonomy is great until it isn't. For anything consequential, you want a human in the loop — and you want a place where the work is *visible*. Soulacy's **Workboard** is a Kanban board where each card is a task, and each task can be handed to an agent to execute.

The lifecycle is exactly what you'd hope: a task moves `todo → running → needs_review → done` (or `failed`). You assign an agent and press Run; the platform dispatches the task to that agent, captures its output and any files it produced as downloadable **artifacts**, records the full run, and parks the result in **needs_review**. A person then reads the output, leaves comments, and approves it — or sends it back. Retries create new attempts without destroying the history of prior ones, so you can see exactly how an agent arrived (or failed to arrive) at an answer.

It's a simple, powerful pattern: the board is the shared surface where autonomous work and human judgment meet. Agents do the doing; people own the approving.

## Where agents live, and when they wake up

Agents are only useful if they're reachable. Soulacy exposes them through **channels** — the always-on HTTP API plus chat platforms like **Telegram, Slack, Discord, and WhatsApp** — so an agent can answer in the tools your team already uses, not just a bespoke web page.

And agents don't have to wait to be asked. The **scheduler** runs cron-based agents on their own cadence — every morning, every fifteen minutes, weekdays only — with sensible handling of missed runs when the gateway was down, so a daily briefing fires once on restart rather than replaying a backlog or silently skipping. Combined with channels, this is what turns an "assistant you chat with" into a "teammate that shows up": it wakes on a schedule, does its job, and posts the result where you'll see it.

## Memory and knowledge: agents that don't start from zero

Two systems keep agents grounded over time.

**Knowledge bases** give agents retrieval over your own documents — an embedded vector store with pluggable embeddings, searchable via a `kb_search` tool. Feed an agent your docs, PDFs, or notes, and it can cite the source material in its answers rather than hallucinating.

**Memory** operates at a different level. Beyond ordinary conversation history, Soulacy supports a layered "brain memory" — episodic (what happened recently), semantic (facts worth recalling), and procedural (operating rules the agent refines over time) — injected into context so an agent improves and stays coherent across sessions instead of waking up amnesiac every time.

## Built to be self-hosted — and to be careful about it

Self-hosting is the whole point, so security isn't an afterthought. Soulacy ships with API-key authentication, role-based access control, rate limiting, and a series of guardrails around the genuinely dangerous capabilities. System tools require explicit, layered authorization. Privileged actions can be gated behind confirmation prompts. Python tools run sandboxed with enforced CPU, memory, and file limits. Secrets live in a credential vault rather than scattered through config.

These aren't features you notice when things go well — they're the difference between "I let an agent run shell commands on my server" being reckless versus reasonable.

## Running it

Operationally, Soulacy is refreshingly boring in the best way. The gateway is a single Go binary with the web UI embedded; the visual builder, dashboard, agent editor, workboard, and config pages all ship inside it. You can run it with one Docker command for a quick local spin-up, or bring up a full stack with Postgres and a vector store via compose. A companion CLI (`sy`) manages the system from the terminal — installing skills, registering MCP servers, inspecting agents, configuring channels — and the gateway exposes a REST API plus a live event stream for anything you want to build on top.

The design goal is that the same platform serves a hobbyist running everything locally on a laptop and a team running it on shared infrastructure, without forcing either into the other's shape.

## Who this is for, and why it matters

Soulacy sits at an intersection that's surprisingly empty. There are slick hosted agent products that you don't control and can't run locally. There are powerful code-first frameworks that demand you build the entire operational layer — UI, scheduling, memory, channels, guardrails — yourself. Soulacy aims to be the platform in between: **the operational substance of a real product, with the openness and control of self-hosting.**

It matters most when any of these are true for you:

- **Your data can't leave.** Run agents entirely on local models; nothing goes to a third party.
- **You want to own your stack.** No vendor lock-in; swap models per task and host it yourself.
- **You need agents that act, not just chat.** Tools, MCP, shell, channels, and a human-in-the-loop board to keep it accountable.
- **You want both speed and control.** Describe an agent in a sentence in Studio, then drop into YAML and the CLI when you need precision.

The broader thesis is simple: the interesting frontier of AI isn't a better chatbot — it's agents that reliably *do things* in your world, on infrastructure you trust, with the right model for each job and a human watching the parts that matter. That's the platform Soulacy is trying to be.

## Getting started

If you want to try it, the fastest path is the local one: bring up the gateway with Docker, point it at a local model through Ollama (or paste in a cloud API key), open Studio, and describe the first agent you'd actually use — a morning digest, a research assistant, a task-runner for your team board. Watch it compile your sentence into a real workflow, run it, and post the result to a channel you already check.

That loop — *describe it, run it, own it* — is the whole idea. Everything else is detail.

---

*Soulacy is an open, self-hostable AI agent platform. If you're building agents and tired of renting someone else's stack, it's worth a look.*
