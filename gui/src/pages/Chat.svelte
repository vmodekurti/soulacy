<script>
  import { onMount, tick } from 'svelte'
  import { api } from '../lib/api.js'
  import { chatAgentId, chatMessages, chatSending } from '../lib/stores.js'

  let agents    = []
  let input     = ''
  let error     = null
  let msgListEl

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
    input = ''
    chatMessages.update(m => [...m, { role: 'user', text, ts: new Date() }])
    chatSending.set(true)
    await scrollBottom()
    try {
      const res = await api.chat($chatAgentId, text)
      chatMessages.update(m => [...m, { role: 'assistant', text: res.reply, ts: new Date() }])
    } catch (e) {
      chatMessages.update(m => [...m, { role: 'system', text: '⚠ ' + e.message, ts: new Date() }])
    }
    chatSending.set(false)
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
  }

  function fmtTime(d) {
    try { return d.toLocaleTimeString() } catch { return '' }
  }

  onMount(async () => {
    await loadAgents()
    // Scroll to bottom when returning to a conversation already in progress
    await scrollBottom()
  })
</script>

<div class="page">
  <div class="page-header">
    <h1>Chat Tester</h1>
    <div class="controls">
      <select bind:value={$chatAgentId} style="width:220px" disabled={!agents.length}>
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
              <div class="btime">{fmtTime(msg.ts)}</div>
            </div>
          </div>
        {/each}
        {#if $chatSending}
          <div class="msg-row">
            <div class="bubble">
              <div class="typing"><span/><span/><span/></div>
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
  .controls    { display: flex; gap: .75rem; align-items: center; }
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
