<script>
  import { onDestroy, onMount, tick } from 'svelte'
  import { api, createEventSocket } from '../lib/api.js'
  import { chatAgentId, chatMessages, chatSending, chatSessionId, connected } from '../lib/stores.js'
  import RunMetrics from '../lib/RunMetrics.svelte'

  let metricsRefresh = 0

  let agents    = []
  let input     = ''
  let error     = null
  let msgListEl
  let ws        = null
  let stopEvents = false
  let activeThinking = null
  let activeRunKey = ''

  function newChatSessionId() {
    return `gui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
  }

  // ── load agents once on mount ────────────────────────────────────────
  async function loadAgents() {
    try {
      const res = await api.agents.list()
      agents = (res.agents || []).filter(a => a.enabled)
      // Pre-select only if nothing is selected yet.
      // Prefer the "system" agent; fall back to the first enabled agent.
      if (agents.length && !$chatAgentId) {
        const sys = agents.find(a => a.id === 'system')
        chatAgentId.set(sys ? sys.id : agents[0].id)
      }
    } catch (e) { error = e.message }
  }

  // ── send a message ───────────────────────────────────────────────────
  // NOTE: this function intentionally uses store setters, not local vars.
  // If the component unmounts mid-request, the async continuation still
  // runs and updates the store; the component picks it up on remount.
  async function send() {
    const text = input.trim()
    if (!text || !$chatAgentId || $chatSending) return
    const runSessionId = $chatSessionId
    const runAgentId = $chatAgentId
    activeRunKey = `${runAgentId}|${runSessionId}`
    activeThinking = { open: true, events: [] }
    input = ''
    chatMessages.update(m => [...m, { role: 'user', text, ts: new Date() }])
    chatSending.set(true)
    await scrollBottom()
    try {
      const res = await api.chat(runAgentId, text, 'gui-user', null, runSessionId)
      chatMessages.update(m => [...m, { role: 'assistant', text: res.reply, ts: new Date(), thinking: activeThinking }])
    } catch (e) {
      chatMessages.update(m => [...m, { role: 'system', text: '⚠ ' + e.message, ts: new Date(), thinking: activeThinking }])
    }
    activeThinking = null
    activeRunKey = ''
    chatSending.set(false)
    metricsRefresh++   // re-fetch the session metrics strip (Story 7)
    await scrollBottom()
  }

  async function scrollBottom() {
    await tick()
    if (msgListEl) msgListEl.scrollTop = msgListEl.scrollHeight
  }

  function onKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
  }

  function clearChat() {
    chatMessages.set([])
    chatSending.set(false)
    chatSessionId.set(newChatSessionId())
    activeThinking = null
    activeRunKey = ''
  }

  function fmtTime(d) {
    try { return d.toLocaleTimeString() } catch { return '' }
  }

  function connectEvents() {
    if (stopEvents) return
    try { ws = createEventSocket() } catch { return }
    ws.onopen = () => { $connected = true }
    ws.onmessage = async (e) => {
      try {
        const ev = JSON.parse(e.data)
        if (!activeThinking || !eventBelongsToActiveRun(ev) || !isThinkingEvent(ev)) return
        activeThinking.events = [...activeThinking.events, ev].slice(-80)
        await scrollBottom()
      } catch {}
    }
    ws.onclose = () => {
      $connected = false
      ws = null
      if (!stopEvents) setTimeout(connectEvents, 3000)
    }
    ws.onerror = () => ws?.close()
  }

  function eventBelongsToActiveRun(ev) {
    if (!activeRunKey) return false
    return `${ev.agent_id || ''}|${ev.session_id || ''}` === activeRunKey
  }

  function isThinkingEvent(ev) {
    return ['llm.call', 'llm.result', 'tool.call', 'tool.result', 'tool.log', 'error'].includes(ev.type || '')
  }

  function toggleThinking(thinking) {
    if (!thinking) return
    thinking.open = !thinking.open
    if (thinking === activeThinking) activeThinking = { ...thinking }
    chatMessages.update(m => [...m])
  }

  function thinkingSummary(thinking) {
    const n = thinking?.events?.length || 0
    if (n === 0) return $connected ? 'Waiting for activity' : 'Connecting to activity stream'
    const tools = thinking.events.filter(e => (e.type || '').startsWith('tool.')).length
    const llm = thinking.events.filter(e => (e.type || '').startsWith('llm.')).length
    const errors = thinking.events.filter(e => (e.type || '').includes('error')).length
    return `${n} event${n === 1 ? '' : 's'} · ${llm} LLM · ${tools} tool${tools === 1 ? '' : 's'}${errors ? ` · ${errors} error${errors === 1 ? '' : 's'}` : ''}`
  }

  function eventTitle(ev) {
    const p = ev.payload || {}
    switch (ev.type) {
      case 'llm.call':    return `Calling ${p.model || 'model'} · turn ${p.turn ?? '?'}`
      case 'llm.result':  return `Model returned ${p.output_tokens ?? 0} output tokens${p.tool_calls ? ` and ${p.tool_calls} tool call${p.tool_calls === 1 ? '' : 's'}` : ''}`
      case 'tool.call':   return `Calling tool ${p.name || 'tool'}`
      case 'tool.result': return `Tool ${p.name || 'tool'} returned`
      case 'tool.log':    return `Tool log`
      case 'error':       return `Error${p.stage ? ` in ${p.stage}` : ''}`
      default:            return ev.type || 'event'
    }
  }

  function eventDetail(ev) {
    const p = ev.payload || {}
    if (ev.type === 'tool.call') return snippet(JSON.stringify(p.arguments || {}), 220)
    if (ev.type === 'tool.result') return snippet(p.content || '', 260)
    if (ev.type === 'tool.log') return snippet(typeof p === 'string' ? p : p.line || JSON.stringify(p), 260)
    if (ev.type === 'error') return snippet(p.error || p.message || JSON.stringify(p), 260)
    if (ev.type === 'llm.result') return `${p.duration_ms ?? 0}ms · ${p.input_tokens ?? 0} in / ${p.output_tokens ?? 0} out`
    return ''
  }

  function eventClass(type = '') {
    if (type.includes('error')) return 'err'
    if (type.startsWith('tool.')) return 'tool'
    if (type.startsWith('llm.')) return 'llm'
    return ''
  }

  function snippet(s, n = 180) {
    s = String(s ?? '')
    return s.length > n ? s.slice(0, n) + '…' : s
  }

  onMount(async () => {
    await loadAgents()
    connectEvents()
    // Scroll to bottom when returning to a conversation already in progress
    await scrollBottom()
  })

  onDestroy(() => {
    stopEvents = true
    if (ws) ws.close()
  })
</script>

<div class="page">
  <div class="page-header">
    <h1>Chat Tester</h1>
    <div class="controls">
      <RunMetrics sessionId={$chatSessionId} agentId={$chatAgentId} refreshKey={metricsRefresh} />
      <select bind:value={$chatAgentId} style="width:min(220px, 100%)" disabled={!agents.length}>
        {#if !agents.length}
          <option value="">No enabled agents</option>
        {:else}
          {#each agents as a}
            <option value={a.id}>{a.name || a.id}</option>
          {/each}
        {/if}
      </select>
      <button class="btn-secondary" on:click={clearChat}>Clear</button>
    </div>
  </div>

  {#if error}
    <div class="banner err">⚠ {error}</div>
  {/if}

  <div class="chat-wrap">
    <!-- Message list -->
    <div class="messages" bind:this={msgListEl}>
      {#if $chatMessages.length === 0}
        <div class="empty">
          {#if $chatAgentId}
            Chatting with <strong>{$chatAgentId}</strong>.<br>Type a message below.
          {:else}
            Select an agent above to start chatting.
          {/if}
        </div>
      {:else}
        {#each $chatMessages as msg}
          <div class="msg-row" class:user={msg.role==='user'} class:sys={msg.role==='system'}>
            <div class="bubble">
              <div class="btext">{msg.text}</div>
              {#if msg.thinking}
                <div class="thinking" class:open={msg.thinking.open}>
                  <button class="thinking-head" type="button" on:click={() => toggleThinking(msg.thinking)}>
                    <span class="chev">{msg.thinking.open ? '▾' : '▸'}</span>
                    <span class="thinking-title">Thinking</span>
                    <span class="thinking-meta">{thinkingSummary(msg.thinking)}</span>
                  </button>
                  {#if msg.thinking.open}
                    <div class="thinking-body">
                      {#if msg.thinking.events.length === 0}
                        <div class="thinking-empty">No activity captured for this run.</div>
                      {:else}
                        {#each msg.thinking.events as ev}
                          <div class="think-event {eventClass(ev.type)}">
                            <div class="think-main">
                              <span class="think-type">{ev.type}</span>
                              <span class="think-text">{eventTitle(ev)}</span>
                            </div>
                            {#if eventDetail(ev)}
                              <div class="think-detail">{eventDetail(ev)}</div>
                            {/if}
                          </div>
                        {/each}
                      {/if}
                    </div>
                  {/if}
                </div>
              {/if}
              <div class="btime">{fmtTime(msg.ts)}</div>
            </div>
          </div>
        {/each}
        {#if $chatSending}
          <div class="msg-row">
            <div class="bubble">
              <div class="typing"><span/><span/><span/></div>
              {#if activeThinking}
                <div class="thinking open">
                  <button class="thinking-head" type="button" on:click={() => toggleThinking(activeThinking)}>
                    <span class="chev">{activeThinking.open ? '▾' : '▸'}</span>
                    <span class="thinking-title">Thinking</span>
                    <span class="thinking-meta">{thinkingSummary(activeThinking)}</span>
                  </button>
                  {#if activeThinking.open}
                    <div class="thinking-body">
                      {#if activeThinking.events.length === 0}
                        <div class="thinking-empty">Waiting for the first runtime event…</div>
                      {:else}
                        {#each activeThinking.events as ev}
                          <div class="think-event {eventClass(ev.type)}">
                            <div class="think-main">
                              <span class="think-type">{ev.type}</span>
                              <span class="think-text">{eventTitle(ev)}</span>
                            </div>
                            {#if eventDetail(ev)}
                              <div class="think-detail">{eventDetail(ev)}</div>
                            {/if}
                          </div>
                        {/each}
                      {/if}
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        {/if}
      {/if}
    </div>

    <!-- Input -->
    <div class="input-row">
      <textarea
        bind:value={input}
        on:keydown={onKeydown}
        placeholder="Send a message… (Enter to send, Shift+Enter for newline)"
        rows="2"
        disabled={$chatSending || !$chatAgentId}
      ></textarea>
      <button class="send-btn btn-primary"
              on:click={send}
              disabled={$chatSending || !$chatAgentId || !input.trim()}>
        {$chatSending ? '…' : '↑'}
      </button>
    </div>
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; height: 100%; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .controls    { display: flex; gap: .75rem; align-items: center; flex-wrap: wrap; min-width: 0; }
  .banner      { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err         { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }

  .chat-wrap {
    flex: 1; min-height: 0;
    background: #141626; border: 1px solid #1a1e36; border-radius: 10px;
    display: flex; flex-direction: column; overflow: hidden;
  }

  .messages {
    flex: 1; overflow-y: auto;
    padding: 1rem; display: flex; flex-direction: column; gap: .75rem;
  }
  .empty { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
           color: #6b7294; text-align: center; line-height: 1.7; gap: .25rem; }

  .msg-row       { display: flex; justify-content: flex-start; }
  .msg-row.user  { justify-content: flex-end; }
  .msg-row.sys   { justify-content: center; }

  .bubble {
    max-width: 66%; padding: .6rem .9rem; border-radius: 12px;
    display: flex; flex-direction: column; gap: .25rem;
    background: #1c1f35; border: 1px solid #2a2f4a;
    border-bottom-left-radius: 3px;
  }
  .user .bubble {
    background: #5b52ef; border-color: transparent;
    color: #fff; border-bottom-left-radius: 12px; border-bottom-right-radius: 3px;
  }
  .sys .bubble  { background: rgba(240,96,96,.1); border-color: rgba(240,96,96,.3); color: #f06060; }

  .btext { font-size: .88rem; white-space: pre-wrap; word-break: break-word; line-height: 1.5; }
  .btime { font-size: .68rem; opacity: .55; align-self: flex-end; }

  .thinking {
    margin-top: .45rem;
    border: 1px solid rgba(108, 99, 255, .26);
    background: rgba(10, 12, 24, .34);
    border-radius: 8px;
    overflow: hidden;
  }
  .thinking-head {
    width: 100%;
    min-height: 32px;
    padding: .35rem .5rem;
    display: grid;
    grid-template-columns: 16px auto 1fr;
    gap: .35rem;
    align-items: center;
    border: 0;
    color: #d6d8ef;
    background: transparent;
    cursor: pointer;
    text-align: left;
  }
  .thinking-head:hover { background: rgba(255,255,255,.04); }
  .chev { color: #8b85ff; font-size: .8rem; line-height: 1; }
  .thinking-title { font-size: .76rem; font-weight: 650; }
  .thinking-meta {
    min-width: 0;
    color: #8f95ba;
    font-size: .72rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    justify-self: end;
  }
  .thinking-body {
    padding: .35rem .45rem .45rem;
    display: flex;
    flex-direction: column;
    gap: .32rem;
    border-top: 1px solid rgba(108, 99, 255, .18);
  }
  .thinking-empty {
    color: #8f95ba;
    font-size: .75rem;
    padding: .25rem .15rem;
  }
  .think-event {
    padding: .38rem .45rem;
    border-radius: 6px;
    background: rgba(255,255,255,.035);
    border-left: 2px solid #6b7294;
  }
  .think-event.llm  { border-left-color: #8b85ff; }
  .think-event.tool { border-left-color: #f0a060; }
  .think-event.err  { border-left-color: #f06060; }
  .think-main {
    display: flex;
    gap: .45rem;
    align-items: baseline;
    min-width: 0;
  }
  .think-type {
    flex: 0 0 auto;
    color: #8f95ba;
    font-size: .66rem;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  .think-text {
    min-width: 0;
    color: #e4e6f7;
    font-size: .75rem;
    line-height: 1.35;
    word-break: break-word;
  }
  .think-detail {
    margin-top: .18rem;
    color: #aeb3d4;
    font-size: .72rem;
    line-height: 1.35;
    white-space: pre-wrap;
    word-break: break-word;
  }

  /* Typing indicator */
  .typing { display: flex; gap: 4px; align-items: center; height: 1.1rem; }
  .typing span {
    width: 6px; height: 6px; border-radius: 50%;
    background: #6b7294; animation: bounce 1.1s infinite;
  }
  .typing span:nth-child(2) { animation-delay: .18s; }
  .typing span:nth-child(3) { animation-delay: .36s; }
  @keyframes bounce {
    0%, 80%, 100% { transform: scale(.65); opacity: .4; }
    40%           { transform: scale(1);   opacity: 1;   }
  }

  .input-row {
    display: flex; gap: .65rem; align-items: flex-end;
    padding: .7rem; border-top: 1px solid #1a1e36; flex-shrink: 0;
  }
  .input-row textarea { flex: 1; resize: none; }
  .send-btn { height: 40px; padding: 0 1rem; font-size: 1rem; align-self: flex-end; flex-shrink: 0; }
</style>
