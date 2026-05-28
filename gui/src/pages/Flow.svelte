<script>
  import { onMount } from 'svelte'
  import { writable } from 'svelte/store'
  import { SvelteFlow, Background, Controls, MiniMap, MarkerType } from '@xyflow/svelte'
  import '@xyflow/svelte/dist/style.css'
  import { api } from '../lib/api.js'

  let agents = []
  let selected = null
  let focused = null      // currently selected node (right-panel inspector)
  let error   = ''
  let loading = true

  // Svelte Flow uses stores
  const nodes = writable([])
  const edges = writable([])

  async function load() {
    loading = true
    try {
      const res = await api.agents.list()
      agents = res.agents || []
      if (!selected && agents.length > 0) {
        pick(agents[0])
      }
    } catch (e) { error = e.message }
    loading = false
  }

  function pick(agent) {
    selected = agent
    focused  = null
    const { ns, es } = buildGraph(agent)
    nodes.set(ns)
    edges.set(es)
  }

  // ── Graph builder ──────────────────────────────────────────────────────────
  // Translate a SOUL.yaml definition into a node/edge graph.
  function buildGraph(a) {
    const ns = []
    const es = []
    const COL_X = { trigger: 40, prompt: 40, memory: 40, llm: 460, tool: 880, out: 1280 }
    const COL_Y_CENTER = 240

    // Trigger
    const triggerLabel = a.trigger === 'cron'
      ? `⏱ Cron — ${a.schedule?.cron || '—'}`
      : a.trigger === 'channel'
        ? `📡 Channel — ${(a.channels || []).join(', ') || 'http'}`
        : `▶ ${a.trigger}`
    ns.push({
      id: 'trigger', type: 'input',
      position: { x: COL_X.trigger, y: 60 },
      data: { label: triggerLabel },
      class: 'cs-node cs-trigger',
    })

    // System Prompt
    const sp = (a.system_prompt || '').trim()
    const spPreview = sp.length > 80 ? sp.slice(0, 80) + '…' : (sp || '(empty)')
    ns.push({
      id: 'prompt',
      position: { x: COL_X.prompt, y: 200 },
      data: { label: `📝 System Prompt\n${spPreview}` },
      class: 'cs-node cs-prompt',
    })

    // Memory
    const mem = a.memory || {}
    const memScopes = (mem.read_scopes || []).length === 0 ? 'none' : (mem.read_scopes || []).join(', ')
    ns.push({
      id: 'memory',
      position: { x: COL_X.memory, y: 380 },
      data: { label: `🧠 Memory\nscope: ${memScopes}` },
      class: 'cs-node cs-memory',
    })

    // LLM (the brain — central)
    const llm = a.llm || {}
    ns.push({
      id: 'llm',
      position: { x: COL_X.llm, y: COL_Y_CENTER },
      data: { label: `🤖 LLM\n${llm.provider || 'ollama'} · ${llm.model || '?'}\ntemp ${llm.temperature ?? 0.7}` },
      class: 'cs-node cs-llm',
    })

    // Tools (one node per)
    const tools = a.tools || []
    if (tools.length === 0) {
      ns.push({
        id: 'no-tools',
        position: { x: COL_X.tool, y: COL_Y_CENTER },
        data: { label: '⚙ No tools wired' },
        class: 'cs-node cs-empty',
      })
      es.push({ id: 'e-llm-notools', source: 'llm', target: 'no-tools',
                animated: false, style: 'stroke: #2a2f4a; stroke-dasharray: 4 4' })
    } else {
      const baseY = COL_Y_CENTER - ((tools.length - 1) * 90) / 2
      tools.forEach((t, i) => {
        const id = `tool-${i}`
        const pf = t.python_file ? t.python_file.replace(/^.*\//, '') : ''
        ns.push({
          id,
          position: { x: COL_X.tool, y: baseY + i * 110 },
          data: { label: `⚙ ${t.name || '(unnamed)'}${pf ? '\n' + pf : ''}` },
          class: 'cs-node cs-tool',
        })
        es.push({ id: `e-llm-${id}`, source: 'llm', target: id, animated: true,
                  markerEnd: { type: MarkerType.ArrowClosed, color: '#4caf82' },
                  style: 'stroke: #4caf82' })
      })
    }

    // Output (where the reply lands)
    const outLabel = a.trigger === 'cron'
      ? '📤 Output\n(scheduled run reply)'
      : `📤 Output\n${(a.channels || []).join(', ') || 'http'}`
    ns.push({
      id: 'output', type: 'output',
      position: { x: COL_X.out, y: COL_Y_CENTER },
      data: { label: outLabel },
      class: 'cs-node cs-output',
    })

    // Wire edges into LLM
    es.push({ id: 'e-trig-llm', source: 'trigger', target: 'llm',
              markerEnd: { type: MarkerType.ArrowClosed, color: '#8b85ff' },
              style: 'stroke: #8b85ff' })
    es.push({ id: 'e-prompt-llm', source: 'prompt', target: 'llm',
              markerEnd: { type: MarkerType.ArrowClosed, color: '#f0c060' },
              style: 'stroke: #f0c060' })
    es.push({ id: 'e-mem-llm', source: 'memory', target: 'llm',
              markerEnd: { type: MarkerType.ArrowClosed, color: '#7b82a8' },
              style: 'stroke: #7b82a8' })
    es.push({ id: 'e-llm-out', source: 'llm', target: 'output',
              animated: true,
              markerEnd: { type: MarkerType.ArrowClosed, color: '#6c63ff' },
              style: 'stroke: #6c63ff; stroke-width: 2' })

    return { ns, es }
  }

  function onNodeClick({ detail }) {
    focused = detail.node
  }

  // Build a friendly inspector body from the focused node + agent
  function inspectorBody(node, agent) {
    if (!node || !agent) return null
    switch (node.id) {
      case 'trigger':
        return {
          title: 'Trigger',
          fields: [
            ['Type',     agent.trigger],
            ['Cron',     agent.schedule?.cron || '—'],
            ['Channels', (agent.channels || []).join(', ') || '—'],
          ],
        }
      case 'prompt':
        return { title: 'System Prompt', textarea: agent.system_prompt }
      case 'memory':
        return {
          title: 'Memory',
          fields: [
            ['Read scopes',  (agent.memory?.read_scopes  || []).join(', ') || 'none'],
            ['Write scopes', (agent.memory?.write_scopes || []).join(', ') || 'none'],
            ['Max tokens',   agent.memory?.max_tokens ?? '—'],
          ],
        }
      case 'llm':
        return {
          title: 'LLM',
          fields: [
            ['Provider',    agent.llm?.provider || '—'],
            ['Model',       agent.llm?.model    || '—'],
            ['Temperature', agent.llm?.temperature ?? '—'],
            ['Max tokens',  agent.llm?.max_tokens  ?? '—'],
            ['Max turns',   agent.max_turns ?? '—'],
          ],
        }
      case 'output':
        return {
          title: 'Output',
          fields: [
            ['Channels', (agent.channels || []).join(', ') || '(scheduled — no channel reply)'],
          ],
        }
      default:
        if (node.id.startsWith('tool-')) {
          const i = parseInt(node.id.slice(5), 10)
          const t = (agent.tools || [])[i]
          if (!t) return null
          return {
            title: `Tool: ${t.name}`,
            fields: [
              ['Name',        t.name],
              ['Description', t.description || '—'],
              ['Python file', t.python_file || '—'],
            ],
            json: t.parameters || {},
          }
        }
        return null
    }
  }

  $: inspector = inspectorBody(focused, selected)

  onMount(load)
</script>

<div class="page">
  <aside class="rail">
    <header class="rail-hdr">
      <h2>Flows</h2>
      <button class="btn-secondary small" on:click={load} disabled={loading}>↺</button>
    </header>
    {#if error}<div class="banner err">{error}</div>{/if}
    {#if agents.length === 0}
      <div class="empty">No agents yet.</div>
    {:else}
      <div class="rail-list">
        {#each agents as a}
          <button class="rail-item" class:active={selected?.id === a.id} on:click={() => pick(a)}>
            <span class="ri-name">{a.name || a.id}</span>
            <span class="ri-meta">{a.trigger} · {a.llm?.provider || 'ollama'} · {(a.tools || []).length} tools</span>
          </button>
        {/each}
      </div>
    {/if}
  </aside>

  <main class="canvas-wrap">
    {#if selected}
      <header class="canvas-hdr">
        <div>
          <span class="canvas-title">{selected.name || selected.id}</span>
          <span class="canvas-sub">{selected.description || ''}</span>
        </div>
        <a class="btn-secondary small" href={'#agents'}>Edit agent →</a>
      </header>
      <div class="canvas">
        <SvelteFlow {nodes} {edges} fitView on:nodeclick={onNodeClick}>
          <Background />
          <Controls />
          <MiniMap />
        </SvelteFlow>
      </div>
    {:else}
      <div class="empty big">Select an agent on the left to visualize its flow.</div>
    {/if}
  </main>

  {#if inspector}
    <aside class="inspector">
      <header class="ins-hdr">
        <span>{inspector.title}</span>
        <button class="ins-close" on:click={() => focused = null}>✕</button>
      </header>
      <div class="ins-body">
        {#if inspector.textarea !== undefined}
          <pre class="ins-pre">{inspector.textarea || '(empty)'}</pre>
        {/if}
        {#if inspector.fields}
          {#each inspector.fields as [k, v]}
            <div class="ins-row">
              <span class="ins-k">{k}</span>
              <span class="ins-v">{v}</span>
            </div>
          {/each}
        {/if}
        {#if inspector.json !== undefined}
          <div class="ins-row" style="flex-direction: column; align-items: flex-start;">
            <span class="ins-k">Parameters</span>
            <pre class="ins-pre">{JSON.stringify(inspector.json, null, 2)}</pre>
          </div>
        {/if}
      </div>
    </aside>
  {/if}
</div>

<style>
  .page    { display: flex; height: 100%; min-height: 100vh; overflow: hidden; }

  /* Left rail */
  .rail    { width: 220px; background: #0e1020; border-right: 1px solid #1a1e36;
             display: flex; flex-direction: column; flex-shrink: 0; }
  .rail-hdr{ display: flex; align-items: center; justify-content: space-between;
             padding: .8rem 1rem; border-bottom: 1px solid #1a1e36; }
  .rail-hdr h2 { font-size: .95rem; font-weight: 600; }
  .small   { padding: .2rem .55rem; font-size: .72rem; }
  .empty   { color: #6b7294; padding: 2rem 1rem; text-align: center; font-size: .85rem; }
  .empty.big { padding: 5rem; }
  .banner  { padding: .5rem .85rem; margin: .5rem; border-radius: 6px; font-size: .8rem; }
  .err     { background: rgba(240,96,96,.1); color: #f06060; border: 1px solid rgba(240,96,96,.3); }

  .rail-list { display: flex; flex-direction: column; gap: .3rem; padding: .5rem; overflow-y: auto; }
  .rail-item { background: #141626; color: #c8cadf; border: 1px solid #1a1e36; border-radius: 7px;
               padding: .55rem .7rem; text-align: left; cursor: pointer;
               display: flex; flex-direction: column; gap: .15rem; transition: border-color .12s; }
  .rail-item:hover { border-color: #2a2f4a; }
  .rail-item.active { border-color: #6c63ff; background: rgba(108,99,255,.08); }
  .ri-name  { font-weight: 600; font-size: .82rem; }
  .ri-meta  { color: #6b7294; font-size: .68rem; }

  /* Canvas */
  .canvas-wrap { flex: 1; display: flex; flex-direction: column; min-width: 0; }
  .canvas-hdr { display: flex; align-items: center; justify-content: space-between;
                padding: .85rem 1.25rem; border-bottom: 1px solid #1a1e36; }
  .canvas-title { font-weight: 600; font-size: .95rem; }
  .canvas-sub   { color: #7b82a8; font-size: .78rem; margin-left: .8rem; }
  .canvas { flex: 1; min-height: 0; background:
              radial-gradient(circle at 50% 50%, #14162a 0%, #0c0e1a 100%); }

  /* xyflow node styling — applied via `class:` on the node objects */
  :global(.cs-node) {
    background: #141626 !important;
    color: #e8eaf6 !important;
    border: 1px solid #1e2240 !important;
    border-radius: 10px !important;
    padding: .55rem .85rem !important;
    font-size: .78rem !important;
    line-height: 1.5 !important;
    min-width: 160px !important;
    box-shadow: 0 4px 14px rgba(0,0,0,.35) !important;
    white-space: pre-line;
  }
  :global(.cs-trigger) { border-color: #6c63ff !important; }
  :global(.cs-prompt)  { border-color: #f0c060 !important; }
  :global(.cs-memory)  { border-color: #555a7a !important; }
  :global(.cs-llm)     { border-color: #6c63ff !important; background: linear-gradient(180deg, #1a1e36 0%, #141626 100%) !important; box-shadow: 0 0 24px rgba(108,99,255,.18) !important; }
  :global(.cs-tool)    { border-color: #4caf82 !important; }
  :global(.cs-output)  { border-color: #6c63ff !important; }
  :global(.cs-empty)   { border-color: #2a2f4a !important; color: #6b7294 !important; font-style: italic; }
  :global(.svelte-flow__attribution) { display: none; }

  /* Inspector */
  .inspector  { width: 320px; background: #0e1020; border-left: 1px solid #1a1e36;
                display: flex; flex-direction: column; flex-shrink: 0; }
  .ins-hdr    { display: flex; align-items: center; justify-content: space-between;
                padding: .8rem 1rem; border-bottom: 1px solid #1a1e36; font-weight: 600; font-size: .88rem; }
  .ins-close  { background: none; color: #6b7294; font-size: 1rem; padding: 0 .25rem; }
  .ins-close:hover { color: #e8eaf6; }
  .ins-body   { padding: .85rem 1rem; overflow-y: auto; display: flex; flex-direction: column; gap: .55rem; }
  .ins-row    { display: flex; gap: .5rem; align-items: flex-start; justify-content: space-between;
                font-size: .8rem; border-bottom: 1px solid #1a1e36; padding-bottom: .35rem; }
  .ins-k      { color: #6b7294; flex-shrink: 0; }
  .ins-v      { color: #c8cadf; text-align: right; word-break: break-all; font-family: monospace; font-size: .76rem; }
  .ins-pre    { background: #0c0e1a; border: 1px solid #1a1e36; border-radius: 6px;
                padding: .55rem .65rem; font-family: monospace; font-size: .72rem;
                color: #b0b5d8; max-height: 360px; overflow: auto; white-space: pre-wrap; line-height: 1.5; }
</style>
