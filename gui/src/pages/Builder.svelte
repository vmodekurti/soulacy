<script>
  import { onDestroy, onMount } from 'svelte'
  import { api } from '../lib/api.js'

  // ── Phase management ────────────────────────────────────────────────────────
  // 'spark'        → initial empty state, centered prompt
  // 'conversation' → chat + live blueprint panel
  // 'generated'    → SOUL.yaml preview + deploy
  let phase = 'spark'

  // ── Session state ───────────────────────────────────────────────────────────
  let sessionId = ''
  let messages  = []           // { role: 'user'|'assistant', text: string }
  let understanding = null     // BuilderUnderstanding from server
  let ready = false            // true when confidence ≥ 0.8 and missing = []

  // ── Generated output ────────────────────────────────────────────────────────
  let soulYAML   = ''
  let deployedId = ''
  let copied     = false

  // ── UI state ────────────────────────────────────────────────────────────────
  let inputText    = ''
  let loading      = false
  let error        = ''
  let messagesEl              // bind:this for scroll-to-bottom
  let inputEl                 // bind:this for auto-focus

  onMount(() => {
    inputEl?.focus()
  })

  // ── Spark → first message ───────────────────────────────────────────────────
  async function startConversation() {
    const text = inputText.trim()
    if (!text) return
    inputText = ''
    phase = 'conversation'
    await sendMessage(text)
  }

  // ── Send a turn ──────────────────────────────────────────────────────────────
  async function sendMessage(text) {
    if (!text || loading) return
    const userText = text.trim()
    if (!userText) return

    messages = [...messages, { role: 'user', text: userText }]
    inputText = ''
    loading   = true
    error     = ''
    scrollToBottom()

    try {
      const res = await api.builder.chat(userText, sessionId)
      sessionId    = res.session_id
      understanding = res.understanding
      ready         = res.ready

      messages = [...messages, { role: 'assistant', text: res.reply }]
      scrollToBottom()
    } catch (e) {
      error = e.message || 'Something went wrong. Is the gateway running?'
    } finally {
      loading = false
    }
  }

  function handleKey(e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      if (phase === 'spark') startConversation()
      else sendMessage(inputText)
    }
  }

  // ── Generate SOUL.yaml ───────────────────────────────────────────────────────
  async function generate() {
    loading = true
    error   = ''
    try {
      const res = await api.builder.generate(sessionId)
      soulYAML = res.soul_yaml
      phase    = 'generated'
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  // ── Deploy agent ─────────────────────────────────────────────────────────────
  async function deploy() {
    loading = true
    error   = ''
    try {
      const res = await api.builder.deploy(sessionId)
      deployedId = res.agent_id
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  // ── Reset ────────────────────────────────────────────────────────────────────
  async function reset() {
    if (sessionId) {
      api.builder.deleteSession(sessionId).catch(() => {})
    }
    phase         = 'spark'
    sessionId     = ''
    messages      = []
    understanding = null
    ready         = false
    soulYAML      = ''
    deployedId    = ''
    inputText     = ''
    error         = ''
  }

  // ── Copy SOUL.yaml ───────────────────────────────────────────────────────────
  async function copyYAML() {
    try {
      await navigator.clipboard.writeText(soulYAML)
      copied = true
      setTimeout(() => { copied = false }, 2000)
    } catch (_) {}
  }

  function scrollToBottom() {
    setTimeout(() => {
      if (messagesEl) messagesEl.scrollTop = messagesEl.scrollHeight
    }, 50)
  }

  // ── Blueprint helpers ────────────────────────────────────────────────────────
  function confidence(u) {
    if (!u) return 0
    return Math.round((u.confidence || 0) * 100)
  }

  function triggerLabel(u) {
    if (!u?.trigger) return '—'
    const t = u.trigger
    if (t.type === 'cron') return `Scheduled: ${t.schedule || 'TBD'}`
    if (t.type === 'channel') return `Channel: ${(t.channels || []).join(', ') || 'http'}`
    if (t.type === 'manual') return 'Manual trigger'
    return '—'
  }

  onDestroy(() => {
    if (sessionId) api.builder.deleteSession(sessionId).catch(() => {})
  })
</script>

<!-- ── SPARK phase ─────────────────────────────────────────────────────────── -->
{#if phase === 'spark'}
  <div class="spark">
    <div class="spark-inner">
      <div class="spark-logo">✦</div>
      <h1 class="spark-title">Build an Agent</h1>
      <p class="spark-sub">Describe what you want in plain English — I'll help you design it.</p>

      <div class="spark-input-wrap">
        <textarea
          bind:this={inputEl}
          bind:value={inputText}
          class="spark-textarea"
          rows="3"
          placeholder="e.g. Every morning, summarise my unread emails and send me a WhatsApp with the 3 most important ones."
          on:keydown={handleKey}
        ></textarea>
        <button class="btn-primary spark-btn" on:click={startConversation} disabled={!inputText.trim()}>
          Start →
        </button>
      </div>

      {#if error}
        <p class="error">{error}</p>
      {/if}
    </div>
  </div>

<!-- ── CONVERSATION phase ─────────────────────────────────────────────────── -->
{:else if phase === 'conversation'}
  <div class="convo-layout">

    <!-- Left: Chat panel -->
    <section class="chat-panel">
      <header class="panel-header">
        <span class="panel-title">✦ Agent Builder</span>
        <button class="small" on:click={reset} title="Start over">↺ Reset</button>
      </header>

      <div class="messages" bind:this={messagesEl}>
        {#each messages as msg}
          <div class="msg" class:user={msg.role === 'user'} class:assistant={msg.role === 'assistant'}>
            {#if msg.role === 'assistant'}
              <span class="msg-avatar">✦</span>
            {/if}
            <div class="msg-bubble">{msg.text}</div>
          </div>
        {/each}

        {#if loading}
          <div class="msg assistant">
            <span class="msg-avatar">✦</span>
            <div class="msg-bubble typing">
              <span class="dot"></span><span class="dot"></span><span class="dot"></span>
            </div>
          </div>
        {/if}
      </div>

      {#if error}
        <p class="chat-error">{error}</p>
      {/if}

      <div class="chat-input-row">
        <textarea
          bind:value={inputText}
          class="chat-input"
          rows="2"
          placeholder="Reply…"
          on:keydown={handleKey}
          disabled={loading}
        ></textarea>
        <button class="btn-primary send-btn"
                on:click={() => sendMessage(inputText)}
                disabled={!inputText.trim() || loading}>
          Send
        </button>
      </div>
    </section>

    <!-- Right: Blueprint panel -->
    <aside class="blueprint-panel">
      <header class="panel-header">
        <span class="panel-title">Blueprint</span>
        {#if ready}
          <span class="badge ready">Ready</span>
        {/if}
      </header>

      <div class="blueprint-body">
        <!-- Confidence bar -->
        <div class="bp-section">
          <span class="bp-label">Confidence</span>
          <div class="conf-bar-track">
            <div class="conf-bar-fill" style="width: {confidence(understanding)}%"
                 class:low={confidence(understanding) < 40}
                 class:mid={confidence(understanding) >= 40 && confidence(understanding) < 80}
                 class:high={confidence(understanding) >= 80}></div>
          </div>
          <span class="conf-pct">{confidence(understanding)}%</span>
        </div>

        {#if understanding}
          <!-- Name + description -->
          <div class="bp-section">
            <span class="bp-label">Name</span>
            <span class="bp-value mono">{understanding.name || '—'}</span>
          </div>
          {#if understanding.description}
            <div class="bp-section">
              <span class="bp-label">Purpose</span>
              <span class="bp-value">{understanding.description}</span>
            </div>
          {/if}

          <!-- Trigger -->
          <div class="bp-section">
            <span class="bp-label">Trigger</span>
            <span class="bp-chip">{triggerLabel(understanding)}</span>
          </div>

          <!-- Tools -->
          {#if understanding.tools?.length}
            <div class="bp-section">
              <span class="bp-label">Tools</span>
              <div class="chip-row">
                {#each understanding.tools as t}
                  <span class="bp-chip">{t.name}</span>
                {/each}
              </div>
            </div>
          {/if}

          <!-- Memory -->
          {#if understanding.memory}
            <div class="bp-section">
              <span class="bp-label">Memory</span>
              <span class="bp-chip">
                {understanding.memory.needs ? (understanding.memory.scope || 'session') : 'none'}
              </span>
            </div>
          {/if}

          <!-- Outputs -->
          {#if understanding.outputs?.length}
            <div class="bp-section">
              <span class="bp-label">Output</span>
              <div class="chip-row">
                {#each understanding.outputs as o}
                  <span class="bp-chip">{o.channel}</span>
                {/each}
              </div>
            </div>
          {/if}

          <!-- Missing -->
          {#if understanding.missing?.length}
            <div class="bp-section">
              <span class="bp-label missing-label">Still needed</span>
              <ul class="missing-list">
                {#each understanding.missing as m}
                  <li>{m}</li>
                {/each}
              </ul>
            </div>
          {/if}
        {:else}
          <p class="bp-empty">Blueprint will appear as we talk…</p>
        {/if}
      </div>

      <div class="blueprint-footer">
        <button class="btn-primary generate-btn"
                disabled={!ready || loading}
                on:click={generate}>
          {loading ? 'Generating…' : 'Generate SOUL.yaml →'}
        </button>
        {#if !ready && understanding}
          <p class="generate-hint">Answer a few more questions to unlock generation.</p>
        {/if}
      </div>
    </aside>
  </div>

<!-- ── GENERATED phase ────────────────────────────────────────────────────── -->
{:else if phase === 'generated'}
  <div class="generated-layout">
    <header class="gen-header">
      <div class="gen-title-row">
        <span class="gen-icon">✦</span>
        <h2 class="gen-title">Agent Ready</h2>
      </div>
      <div class="gen-actions">
        <button class="btn-secondary" on:click={reset}>← Start over</button>
        <button class="btn-secondary" on:click={() => { phase = 'conversation' }}>← Back to chat</button>
      </div>
    </header>

    {#if deployedId}
      <div class="deploy-success">
        ✓ Agent <strong>{deployedId}</strong> deployed successfully. Find it in the Agents tab.
      </div>
    {:else}
      <div class="yaml-card">
        <div class="yaml-header">
          <span class="yaml-label">SOUL.yaml</span>
          <div class="yaml-btns">
            <button class="btn-secondary small-btn" on:click={copyYAML}>
              {copied ? '✓ Copied' : 'Copy'}
            </button>
            <button class="btn-primary small-btn"
                    on:click={deploy}
                    disabled={loading}>
              {loading ? 'Deploying…' : 'Deploy Agent'}
            </button>
          </div>
        </div>
        <pre class="yaml-code">{soulYAML}</pre>
      </div>

      {#if error}
        <p class="error">{error}</p>
      {/if}

      <p class="yaml-hint">
        Review the SOUL.yaml above, then click <strong>Deploy Agent</strong> to register it with Soulacy.
        You can also copy the YAML and place it in your agents directory manually.
      </p>
    {/if}
  </div>
{/if}

<style>
  /* ── Shared ──────────────────────────────────────────────────────────────── */
  .error { color: #e57373; font-size: 0.82rem; margin-top: 0.5rem; }

  /* ── Spark phase ─────────────────────────────────────────────────────────── */
  .spark {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    padding: 2rem;
    background: radial-gradient(ellipse at 50% 40%, rgba(108,99,255,0.07) 0%, transparent 70%);
  }
  .spark-inner {
    width: 100%;
    max-width: 600px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1.2rem;
    text-align: center;
  }
  .spark-logo { font-size: 2.5rem; color: #6c63ff; line-height: 1; }
  .spark-title { font-size: 1.6rem; font-weight: 700; letter-spacing: -0.02em; }
  .spark-sub { color: #7b82a8; font-size: 0.92rem; line-height: 1.6; }

  .spark-input-wrap {
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }
  .spark-textarea {
    width: 100%;
    background: #141626;
    border: 1px solid #2a2f4a;
    border-radius: 10px;
    color: #e8eaf6;
    font-size: 0.95rem;
    padding: 0.85rem 1rem;
    line-height: 1.55;
    resize: none;
    outline: none;
    transition: border-color 0.15s;
  }
  .spark-textarea:focus { border-color: #6c63ff; box-shadow: 0 0 0 2px rgba(108,99,255,0.15); }
  .spark-btn { align-self: flex-end; padding: 0.5rem 1.4rem; font-size: 0.9rem; }

  /* ── Conversation layout ─────────────────────────────────────────────────── */
  .convo-layout {
    display: flex;
    height: 100vh;
    overflow: hidden;
  }

  /* ── Chat panel ──────────────────────────────────────────────────────────── */
  .chat-panel {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    border-right: 1px solid #1a1e36;
  }

  .panel-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.85rem 1.25rem;
    border-bottom: 1px solid #1a1e36;
    flex-shrink: 0;
  }
  .panel-title { font-weight: 600; font-size: 0.88rem; color: #8b85ff; }

  .small { font-size: 0.78rem; color: #6b7294; padding: 0.25rem 0.5rem; background: none; }
  .small:hover { color: #e8eaf6; }

  .messages {
    flex: 1;
    overflow-y: auto;
    padding: 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .msg {
    display: flex;
    gap: 0.6rem;
    align-items: flex-start;
    max-width: 85%;
  }
  .msg.user {
    align-self: flex-end;
    flex-direction: row-reverse;
  }
  .msg-avatar {
    font-size: 0.9rem;
    color: #6c63ff;
    margin-top: 0.25rem;
    flex-shrink: 0;
  }
  .msg-bubble {
    background: #141626;
    border: 1px solid #1e2240;
    border-radius: 10px;
    padding: 0.65rem 0.9rem;
    font-size: 0.875rem;
    line-height: 1.6;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .msg.user .msg-bubble {
    background: rgba(108,99,255,0.12);
    border-color: rgba(108,99,255,0.25);
  }

  /* Typing indicator */
  .typing {
    display: flex;
    gap: 4px;
    align-items: center;
    padding: 0.6rem 0.85rem;
  }
  .dot {
    width: 6px; height: 6px;
    border-radius: 50%;
    background: #6c63ff;
    animation: bounce 1.2s infinite ease-in-out;
  }
  .dot:nth-child(2) { animation-delay: 0.2s; }
  .dot:nth-child(3) { animation-delay: 0.4s; }
  @keyframes bounce {
    0%, 60%, 100% { transform: translateY(0); opacity: 0.6; }
    30% { transform: translateY(-5px); opacity: 1; }
  }

  .chat-error { color: #e57373; font-size: 0.8rem; padding: 0 1.25rem 0.5rem; }

  .chat-input-row {
    display: flex;
    gap: 0.5rem;
    padding: 0.85rem 1.25rem;
    border-top: 1px solid #1a1e36;
    flex-shrink: 0;
  }
  .chat-input {
    flex: 1;
    min-height: 52px;
    max-height: 120px;
    resize: none;
  }
  .send-btn { padding: 0 1.1rem; align-self: flex-end; height: 52px; }

  /* ── Blueprint panel ─────────────────────────────────────────────────────── */
  .blueprint-panel {
    width: 300px;
    flex-shrink: 0;
    display: flex;
    flex-direction: column;
    background: #0e1020;
  }

  .badge {
    font-size: 0.7rem;
    padding: 0.15rem 0.5rem;
    border-radius: 999px;
    font-weight: 600;
  }
  .badge.ready { background: rgba(76,175,130,0.15); color: #4caf82; }

  .blueprint-body {
    flex: 1;
    overflow-y: auto;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.9rem;
  }

  .bp-section { display: flex; flex-direction: column; gap: 0.3rem; }
  .bp-label { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.06em; color: #555a7a; font-weight: 600; }
  .bp-value { font-size: 0.82rem; color: #c8cadf; line-height: 1.5; }
  .bp-value.mono { font-family: 'JetBrains Mono', monospace; color: #8b85ff; }
  .bp-chip {
    display: inline-block;
    background: #1a1e36;
    border: 1px solid #2a2f4a;
    border-radius: 4px;
    font-size: 0.77rem;
    padding: 0.2rem 0.5rem;
    color: #b0b5d8;
  }
  .chip-row { display: flex; flex-wrap: wrap; gap: 0.35rem; }
  .missing-label { color: #e57373; }
  .missing-list { list-style: none; display: flex; flex-direction: column; gap: 0.25rem; }
  .missing-list li { font-size: 0.8rem; color: #e57373; padding-left: 0.75rem; position: relative; }
  .missing-list li::before { content: '·'; position: absolute; left: 0; }

  .bp-empty { color: #3d4166; font-size: 0.82rem; text-align: center; padding: 2rem 0; }

  /* Confidence bar */
  .conf-bar-track {
    height: 4px;
    background: #1a1e36;
    border-radius: 999px;
    overflow: hidden;
    margin: 0.25rem 0;
  }
  .conf-bar-fill {
    height: 100%;
    border-radius: 999px;
    transition: width 0.4s ease;
  }
  .conf-bar-fill.low  { background: #7f3030; }
  .conf-bar-fill.mid  { background: #8a6a00; }
  .conf-bar-fill.high { background: #2e7d5a; }
  .conf-pct { font-size: 0.72rem; color: #555a7a; }

  .blueprint-footer {
    padding: 1rem;
    border-top: 1px solid #1a1e36;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .generate-btn { width: 100%; }
  .generate-hint { font-size: 0.75rem; color: #555a7a; text-align: center; }

  /* ── Generated phase ──────────────────────────────────────────────────────── */
  .generated-layout {
    flex: 1;
    padding: 2rem;
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
    max-width: 860px;
    margin: 0 auto;
    width: 100%;
  }

  .gen-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.75rem;
  }
  .gen-title-row { display: flex; align-items: center; gap: 0.6rem; }
  .gen-icon { font-size: 1.4rem; color: #6c63ff; }
  .gen-title { font-size: 1.3rem; font-weight: 700; }
  .gen-actions { display: flex; gap: 0.5rem; }

  .deploy-success {
    background: rgba(76,175,130,0.1);
    border: 1px solid rgba(76,175,130,0.3);
    border-radius: 8px;
    padding: 1rem 1.25rem;
    color: #4caf82;
    font-size: 0.9rem;
  }

  .yaml-card {
    background: #0e1020;
    border: 1px solid #1a1e36;
    border-radius: 10px;
    overflow: hidden;
  }
  .yaml-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.65rem 1rem;
    border-bottom: 1px solid #1a1e36;
  }
  .yaml-label { font-size: 0.78rem; color: #555a7a; font-weight: 600; text-transform: uppercase; letter-spacing: 0.06em; }
  .yaml-btns { display: flex; gap: 0.5rem; }
  .small-btn { padding: 0.3rem 0.75rem; font-size: 0.8rem; }

  .yaml-code {
    font-family: 'JetBrains Mono', 'Fira Code', monospace;
    font-size: 0.8rem;
    line-height: 1.65;
    color: #b0b5d8;
    padding: 1.1rem 1.25rem;
    overflow-x: auto;
    white-space: pre;
  }

  .yaml-hint { font-size: 0.8rem; color: #555a7a; line-height: 1.6; }
</style>
