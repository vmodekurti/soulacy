<script>
  import { onDestroy, onMount, tick } from 'svelte'
  import { api, apiFetch, createEventSocket } from '../lib/api.js'
  import { chatActiveThreadId, chatThreads, connected } from '../lib/stores.js'
  import RunMetrics from '../lib/RunMetrics.svelte'
  import { entryIdForMessage, nextBranchLabel, entriesToMessages } from '../lib/chatbranch.js'
  import { deltaMetrics, deltaLabel, deltaTitle } from '../lib/chatmetrics.js'
  import { parseMarkdown, richRenderer } from '../lib/markdown.js'
  import {
    nextVoiceState, realtimeCallURL, classifyRealtimeEvent,
    addUsage, voiceUsageLabel, voiceHint,
  } from '../lib/voice.js'

  let metricsRefresh = 0
  let forking = false
  let activeThread = null
  let activeRuns = {}
  let threads = []
  let visibleMessages = []
  let isSending = false

  $: activeThread = $chatActiveThreadId ? ($chatThreads[$chatActiveThreadId] || null) : null
  $: threads = Object.values($chatThreads).sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0))
  $: visibleMessages = activeThread?.messages || []
  $: isSending = !!activeThread?.sending

  function newChatSessionId() {
    return `gui-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
  }

  function newThread(agentId = '') {
    const now = Date.now()
    return {
      id: `thread-${now}-${Math.random().toString(36).slice(2, 8)}`,
      agentId,
      sessionId: newChatSessionId(),
      title: agentId || 'New chat',
      messages: [],
      sending: false,
      branches: [],
      branchMessages: {},
      metricsBaseline: {},
      thinking: null,
      activeRunKey: '',
      createdAt: now,
      updatedAt: now,
    }
  }

  function upsertThread(thread) {
    chatThreads.update(ts => ({ ...ts, [thread.id]: thread }))
  }

  function updateThread(threadId, fn) {
    chatThreads.update(ts => {
      const curr = ts[threadId]
      if (!curr) return ts
      return { ...ts, [threadId]: { ...fn(curr), updatedAt: Date.now() } }
    })
  }

  function updateActiveThread(fn) {
    if (!$chatActiveThreadId) return
    updateThread($chatActiveThreadId, fn)
  }

  function agentName(id) {
    const a = agents.find(x => x.id === id)
    return a?.name || id || 'No agent'
  }

  function selectThread(id) {
    if (!id || !$chatThreads[id]) return
    chatActiveThreadId.set(id)
    metricsRefresh++
    scrollBottom()
  }

  function startThread(agentId = '') {
    const thread = newThread(agentId || activeThread?.agentId || defaultAgentId())
    upsertThread(thread)
    chatActiveThreadId.set(thread.id)
    metricsRefresh++
    scrollBottom()
  }

  function defaultAgentId() {
    const sys = agents.find(a => a.id === 'system')
    return sys ? sys.id : (agents[0]?.id || '')
  }

  function setActiveAgent(agentId) {
    if (!activeThread) {
      startThread(agentId)
      return
    }
    updateActiveThread(t => ({
      ...t,
      agentId,
      title: t.messages.length ? t.title : agentName(agentId),
    }))
  }

  // ── checkpoints & branching (Story 8) ───────────────────────────────
  async function forkAt(mi) {
    if (forking || isSending || !activeThread) return
    forking = true
    error = null
    const threadId = activeThread.id
    try {
      const hist = await api.history.get(activeThread.sessionId)
      const entryId = entryIdForMessage(hist.entries || [], activeThread.messages, mi)
      if (!entryId) {
        error = 'This message has no saved history yet — finish the turn first.'
        return
      }
      const res = await api.history.fork(activeThread.sessionId, {
        agent_id: activeThread.agentId,
        upto_entry_id: entryId,
      })

      // Register branches (current session becomes "main" on first fork).
      let branches = activeThread.branches || []
      if (branches.length === 0) {
        branches = [{ sessionId: activeThread.sessionId, label: 'main' }]
      }
      const label = nextBranchLabel(branches)
      branches = [...branches, { sessionId: res.session_id, label }]

      // Snapshot the current branch, then switch to the fork.
      const forkedView = activeThread.messages.slice(0, mi + 1)
      updateThread(threadId, t => ({
        ...t,
        branches,
        branchMessages: { ...(t.branchMessages || {}), [t.sessionId]: t.messages },
        sessionId: res.session_id,
        messages: forkedView,
      }))
      metricsRefresh++
    } catch (e) {
      error = e.message || 'Fork failed'
    } finally {
      forking = false
    }
  }

  async function switchBranch(sessionId) {
    if (!activeThread || sessionId === activeThread.sessionId || isSending) return
    error = null
    const threadId = activeThread.id
    let msgs = activeThread.branchMessages?.[sessionId]
    if (!msgs) {
      try {
        const hist = await api.history.get(sessionId)
        msgs = entriesToMessages(hist.entries)
      } catch { msgs = [] }
    }
    updateThread(threadId, t => ({
      ...t,
      branchMessages: { ...(t.branchMessages || {}), [t.sessionId]: t.messages },
      sessionId,
      messages: msgs || [],
    }))
    metricsRefresh++
    await scrollBottom()
  }

  let agents    = []
  let input     = ''
  let error     = null
  let msgListEl
  let ws        = null
  let stopEvents = false
  let confirmRequest = null

  // ── load agents once on mount ────────────────────────────────────────
  async function loadAgents() {
    try {
      const res = await api.agents.list()
      // Interface-aware (Stories #11/#12): only show agents meant for Chat —
      // a cron-only agent is hidden unless it explicitly supports chat. The
      // server reports chat_eligible per agent in `interfaces`.
      const iface = (res && res.interfaces) || {}
      const chatOK = (a) => {
        const m = iface[a.id]
        if (m && typeof m.chat_eligible === 'boolean') return m.chat_eligible
        // Fallback for older servers: hide pure cron/oneshot agents.
        return a.trigger !== 'cron' && a.trigger !== 'oneshot'
      }
      agents = (res.agents || []).filter(a => a.enabled && chatOK(a))
      if (agents.length && Object.keys($chatThreads).length === 0) {
        startThread(defaultAgentId())
      }
    } catch (e) { error = e.message }
  }

  // ── send a message ───────────────────────────────────────────────────
  // NOTE: this function intentionally uses store setters, not local vars.
  // If the component unmounts mid-request, the async continuation still
  // runs and updates the store; the component picks it up on remount.
  async function send() {
    const text = input.trim()
    if (!text || !activeThread?.agentId || isSending) return
    const threadId = activeThread.id
    const runSessionId = activeThread.sessionId
    const runAgentId = activeThread.agentId
    const runKey = `${runAgentId}|${runSessionId}`
    const thinking = { open: true, events: [] }
    activeRuns = { ...activeRuns, [runKey]: threadId }
    input = ''
    updateThread(threadId, t => ({
      ...t,
      title: t.messages.length ? t.title : snippet(text, 36),
      messages: [...t.messages, { role: 'user', text, ts: new Date() }],
      sending: true,
      thinking,
      activeRunKey: runKey,
    }))
    await scrollBottom()

    // Pre-turn metrics snapshot for the token delta (Story 9). Cached per
    // session; the first turn fetches (404 → null baseline = "all new").
    let preTurn = activeThread.metricsBaseline?.[runSessionId] ?? null
    if (preTurn === null) {
      preTurn = await api.runs.metrics(runSessionId, runAgentId).catch(() => null)
    }

    try {
      const res = await api.chat(runAgentId, text, 'gui-user', null, runSessionId)
      const curr = await api.runs.metrics(runSessionId, runAgentId).catch(() => null)
      const delta = deltaMetrics(preTurn, curr)
      updateThread(threadId, t => ({
        ...t,
        metricsBaseline: curr ? { ...(t.metricsBaseline || {}), [runSessionId]: curr } : (t.metricsBaseline || {}),
        messages: [...t.messages, { role: 'assistant', text: res.reply, parts: (res.parts || []).filter(p => p && p.type && p.type !== 'text'), ts: new Date(), thinking: t.thinking || thinking, metrics: delta }],
      }))
    } catch (e) {
      updateThread(threadId, t => ({
        ...t,
        messages: [...t.messages, { role: 'system', text: '⚠ ' + e.message, ts: new Date(), thinking: t.thinking || thinking }],
      }))
    }
    updateThread(threadId, t => ({ ...t, sending: false, thinking: null, activeRunKey: '' }))
    const { [runKey]: _, ...rest } = activeRuns
    activeRuns = rest
    metricsRefresh++   // re-fetch the session metrics strip (Story 7)
    await scrollBottom()
  }

  // Cancel the active run (Story #22). The run is registered server-side under
  // the session id, so we cancel by that. Best-effort; the in-flight send()
  // continuation will surface the cancellation as a system message.
  async function cancelSend() {
    const sid = activeThread?.sessionId
    if (!sid) return
    try { await api.cancelRun(sid) } catch (_) { /* already finished */ }
  }

  // Resolve a typed message part to a usable src: prefer a URL, else inline the
  // base64 data as a data: URI (Stories #26/#28).
  function partSrc(part) {
    if (!part) return ''
    if (part.url) return part.url
    if (part.data) {
      const mime = part.mime_type || (part.type === 'image' ? 'image/png' : part.type === 'audio' ? 'audio/mpeg' : 'application/octet-stream')
      return `data:${mime};base64,${part.data}`
    }
    return ''
  }

  async function scrollBottom() {
    await tick()
    if (msgListEl) msgListEl.scrollTop = msgListEl.scrollHeight
  }

  function onKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
  }

  async function resolveConfirm(approved) {
    if (!confirmRequest) return
    try {
      await apiFetch('/chat/confirm', {
        method: 'POST',
        body: JSON.stringify({
          call_id: confirmRequest.call_id,
          approved
        })
      })
    } catch (e) {
      console.error('Failed to confirm tool:', e)
    } finally {
      confirmRequest = null
    }
  }

  function clearChat() {
    if (!activeThread) return
    const replacement = newThread(activeThread.agentId)
    chatThreads.update(ts => {
      const copy = { ...ts }
      delete copy[activeThread.id]
      copy[replacement.id] = replacement
      return copy
    })
    chatActiveThreadId.set(replacement.id)
    metricsRefresh++
  }

  function closeThread(id, e) {
    if (e) e.stopPropagation()
    let nextId = $chatActiveThreadId
    chatThreads.update(ts => {
      const copy = { ...ts }
      delete copy[id]
      if (nextId === id) {
        const remaining = Object.values(copy).sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0))
        nextId = remaining.length > 0 ? remaining[0].id : null
      }
      return copy
    })
    if ($chatActiveThreadId !== nextId) {
      chatActiveThreadId.set(nextId)
      metricsRefresh++
    }
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
        if (ev.type === 'tool_confirm') {
          confirmRequest = ev.payload
          return
        }
        const threadId = threadIdForRunEvent(ev)
        if (!threadId || !isThinkingEvent(ev)) return
        updateThread(threadId, t => {
          if (!t.thinking) return t
          return { ...t, thinking: { ...t.thinking, events: [...t.thinking.events, ev].slice(-80) } }
        })
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

  function threadIdForRunEvent(ev) {
    const key = `${ev.agent_id || ''}|${ev.session_id || ''}`
    return activeRuns[key] || ''
  }

  function isThinkingEvent(ev) {
    return ['llm.call', 'llm.result', 'tool.call', 'tool.result', 'tool.log', 'error',
            'reasoning.start', 'reasoning.step', 'reasoning.result'].includes(ev.type || '')
  }

  function toggleThinking(thinking) {
    if (!thinking) return
    thinking.open = !thinking.open
    updateActiveThread(t => ({ ...t, messages: [...t.messages] }))
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
      case 'reasoning.start':  return `Reasoning loop started (${p.strategy || '?'})`
      case 'reasoning.step':   return `Step ${p.index ?? '?'}${p.tool ? ` → ${p.tool}` : ''}`
      case 'reasoning.result': return `Reasoning finished — ${p.steps ?? 0} step${p.steps === 1 ? '' : 's'}`
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
    if (ev.type === 'reasoning.step') return snippet(p.thought || '', 220)
    if (ev.type === 'reasoning.result') return `${p.duration_ms ?? 0}ms · ${p.confident ? 'confident' : 'not confident'}`
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

  // ── realtime voice panel (Story 11, docs/VOICE_SPIKE.md) ─────────────
  // Audio flows browser↔provider directly over WebRTC with an ephemeral
  // key minted by the gateway; transcripts attach to this chat session.
  let voiceState  = 'unavailable'
  let voiceDetail = ''
  let voiceModel  = ''
  let voiceUsage  = null
  let voicePC     = null   // RTCPeerConnection
  let voiceMic    = null   // MediaStream
  let voiceAudio  = null   // <audio> element for remote playback
  let voiceDraftIdx = -1   // index of the streaming assistant bubble

  async function loadVoiceStatus() {
    try {
      const st = await api.voice.status()
      voiceDetail = st.detail || ''
      voiceModel = st.model || ''
      voiceState = nextVoiceState(voiceState, { type: 'status', available: !!st.available })
    } catch {
      voiceState = 'unavailable'
    }
  }

  function voicePush(role, text, opts = {}) {
    let idx = -1
    updateActiveThread(t => {
      idx = t.messages.length
      return { ...t, messages: [...t.messages, { role, text, voice: true, ts: new Date(), ...opts }] }
    })
    scrollBottom()
    return idx
  }

  function handleVoiceEvent(raw) {
    let evt
    try { evt = JSON.parse(raw) } catch { return }
    const e = classifyRealtimeEvent(evt)
    if (e.kind === 'user_transcript' && e.text.trim()) {
      voicePush('user', e.text.trim())
    } else if (e.kind === 'assistant_delta' && e.text) {
      if (voiceDraftIdx < 0) {
        voiceDraftIdx = voicePush('assistant', e.text)
      } else {
        updateActiveThread(t => {
          const copy = [...t.messages]
          copy[voiceDraftIdx] = { ...copy[voiceDraftIdx], text: copy[voiceDraftIdx].text + e.text }
          return { ...t, messages: copy }
        })
      }
    } else if (e.kind === 'assistant_done') {
      if (voiceDraftIdx >= 0 && e.text) {
        updateActiveThread(t => {
          const copy = [...t.messages]
          copy[voiceDraftIdx] = { ...copy[voiceDraftIdx], text: e.text }
          return { ...t, messages: copy }
        })
      }
      voiceDraftIdx = -1
      scrollBottom()
    } else if (e.kind === 'usage') {
      voiceUsage = addUsage(voiceUsage, e.usage)
    }
  }

  async function startVoice() {
    if (voiceState !== 'idle') return
    voiceState = nextVoiceState(voiceState, { type: 'start' })
    error = null
    try {
      const eph = await api.voice.ephemeral()
      voiceMic = await navigator.mediaDevices.getUserMedia({ audio: true })

      const pc = new RTCPeerConnection()
      voicePC = pc
      for (const track of voiceMic.getTracks()) pc.addTrack(track, voiceMic)
      pc.ontrack = (ev) => {
        if (!voiceAudio) voiceAudio = new Audio()
        voiceAudio.srcObject = ev.streams[0]
        voiceAudio.play().catch(() => {})
      }
      const dc = pc.createDataChannel('oai-events')
      dc.onmessage = (ev) => handleVoiceEvent(ev.data)
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === 'connected') {
          voiceState = nextVoiceState(voiceState, { type: 'connected' })
        } else if (['failed', 'disconnected', 'closed'].includes(pc.connectionState) && voiceState === 'live') {
          stopVoice()
        }
      }

      const offer = await pc.createOffer()
      await pc.setLocalDescription(offer)
      const resp = await fetch(realtimeCallURL(eph.model), {
        method: 'POST',
        headers: { Authorization: `Bearer ${eph.key}`, 'Content-Type': 'application/sdp' },
        body: offer.sdp,
      })
      if (!resp.ok) throw new Error(`provider SDP exchange failed (${resp.status})`)
      await pc.setRemoteDescription({ type: 'answer', sdp: await resp.text() })
      voicePush('system', `🎤 voice session started (${eph.model})`)
    } catch (e) {
      voiceDetail = e.message || 'voice session failed'
      voiceState = nextVoiceState(voiceState, { type: 'fail' })
      teardownVoice()
    }
  }

  function teardownVoice() {
    if (voicePC) { try { voicePC.close() } catch {} voicePC = null }
    if (voiceMic) { for (const t of voiceMic.getTracks()) t.stop(); voiceMic = null }
    if (voiceAudio) { voiceAudio.srcObject = null }
    voiceDraftIdx = -1
  }

  function stopVoice() {
    teardownVoice()
    if (voiceState === 'live' || voiceState === 'connecting') {
      voicePush('system', '🎤 voice session ended')
    }
    voiceState = nextVoiceState(voiceState, { type: 'stop' })
  }

  function voiceClick() {
    if (voiceState === 'idle') startVoice()
    else if (voiceState === 'live' || voiceState === 'connecting') stopVoice()
    else if (voiceState === 'error') voiceState = nextVoiceState(voiceState, { type: 'retry' })
  }

  onMount(async () => {
    await loadAgents()
    await loadVoiceStatus()
    connectEvents()
    // Scroll to bottom when returning to a conversation already in progress
    await scrollBottom()
  })

  onDestroy(() => {
    stopEvents = true
    if (ws) ws.close()
    teardownVoice()
  })
</script>

{#if confirmRequest}
  <div class="confirm-modal-backdrop">
    <div class="confirm-modal">
      <h2>Action Required</h2>
      <p>The agent wants to use the tool <strong>{confirmRequest.tool}</strong>.</p>
      
      {#if confirmRequest.reason}
        <div class="reason-box">
          <strong>Reason:</strong> {confirmRequest.reason}
        </div>
      {/if}

      <div class="args-box">
        <strong>Arguments:</strong>
        <pre>{JSON.stringify(confirmRequest.args, null, 2)}</pre>
      </div>

      <div class="confirm-actions">
        <button class="btn btn-danger" on:click={() => resolveConfirm(false)}>Deny</button>
        <button class="btn btn-primary" on:click={() => resolveConfirm(true)}>Approve</button>
      </div>
    </div>
  </div>
{/if}

<div class="page">
  <div class="page-header">
    <h1>Chat Tester</h1>
    <div class="controls">
      {#if activeThread}
        <RunMetrics sessionId={activeThread.sessionId} agentId={activeThread.agentId} refreshKey={metricsRefresh} />
      {/if}
      <select value={activeThread?.agentId || ''} on:change={(e) => setActiveAgent(e.currentTarget.value)} style="width:min(220px, 100%)" disabled={!agents.length || isSending}>
        {#if !agents.length}
          <option value="">No enabled agents</option>
        {:else}
          {#each agents as a}
            <option value={a.id}>{a.name || a.id}</option>
          {/each}
        {/if}
      </select>
      <button class="btn-secondary" on:click={() => startThread()} disabled={!agents.length}>New chat tab</button>
      <button class="btn-secondary" on:click={clearChat}>Clear</button>
      <button class="voice-btn {voiceState}"
              on:click={voiceClick}
              disabled={voiceState === 'unavailable'}
              title={voiceHint(voiceState, voiceDetail)}
              aria-label="Voice conversation: {voiceHint(voiceState, voiceDetail)}">
        {#if voiceState === 'live'}⏹ 🎤{:else if voiceState === 'connecting'}⏳ 🎤{:else if voiceState === 'error'}⚠ 🎤{:else}🎤{/if}
      </button>
      {#if voiceUsageLabel(voiceUsage)}
        <span class="voice-usage" title="Realtime voice tokens this session ({voiceModel})">
          🎤 {voiceUsageLabel(voiceUsage)}
        </span>
      {/if}
    </div>
  </div>

  {#if error}
    <div class="banner err">⚠ {error}</div>
  {/if}

  {#if threads.length > 0}
    <div class="threads" role="tablist" aria-label="Parallel chats">
      {#each threads as t (t.id)}
        <div class="thread-chip" class:active={t.id === $chatActiveThreadId}
             role="tab" tabindex="0" aria-selected={t.id === $chatActiveThreadId}
             on:click={() => selectThread(t.id)}
             on:keydown={(e) => { if(e.key === 'Enter') selectThread(t.id) }}
             title="{agentName(t.agentId)} · {t.sessionId}">
          <span class="thread-title">{t.title || agentName(t.agentId)}</span>
          <span class="thread-agent">{agentName(t.agentId)}</span>
          {#if t.sending}<span class="thread-dot" aria-label="Running"></span>{/if}
          <button class="thread-close" on:click|stopPropagation={(e) => closeThread(t.id, e)} aria-label="Close tab">✕</button>
        </div>
      {/each}
    </div>
  {/if}

  {#if activeThread?.branches?.length > 0}
    <div class="branches" role="tablist" aria-label="Conversation branches">
      {#each activeThread.branches as b (b.sessionId)}
        <button class="branch-chip" class:active={b.sessionId === activeThread.sessionId}
                role="tab" aria-selected={b.sessionId === activeThread.sessionId}
                on:click={() => switchBranch(b.sessionId)}
                title="Switch to {b.label}">
          ⑂ {b.label}
        </button>
      {/each}
    </div>
  {/if}

  <div class="chat-wrap">
    <!-- Message list -->
    <div class="messages" bind:this={msgListEl}>
      {#if visibleMessages.length === 0}
        <div class="empty">
          {#if activeThread?.agentId}
            Chatting with <strong>{agentName(activeThread.agentId)}</strong>.<br>Type a message below.
          {:else}
            Select an agent above to start chatting.
          {/if}
        </div>
      {:else}
        {#each visibleMessages as msg, mi}
          <div class="msg-row" class:user={msg.role==='user'} class:sys={msg.role==='system'}>
            <div class="bubble">
              {#if msg.role === 'user' || msg.role === 'assistant'}
                <button class="fork-btn" on:click={() => forkAt(mi)} disabled={forking || isSending}
                        title="Fork the conversation from this message"
                        aria-label="Fork conversation from message {mi + 1}">⑂</button>
              {/if}
              {#if msg.role === 'user'}
                <div class="btext">{msg.text}</div>
              {:else}
                <div class="btext markdown-body" use:richRenderer={msg.text}>{@html parseMarkdown(msg.text)}</div>
              {/if}
              {#if msg.parts && msg.parts.length}
                <div class="msg-parts">
                  {#each msg.parts as part}
                    {#if part.type === 'image'}
                      <img class="part-image" src={partSrc(part)} alt={part.name || 'image'} />
                    {:else if part.type === 'audio'}
                      <audio class="part-audio" controls src={partSrc(part)}></audio>
                    {:else if part.type === 'file'}
                      <a class="part-file" href={partSrc(part)} target="_blank" rel="noopener" download>
                        📎 {part.name || part.url || 'Download file'}
                      </a>
                    {/if}
                  {/each}
                </div>
              {/if}
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
              <div class="bmeta">
                <span class="btime">{fmtTime(msg.ts)}</span>
                {#if msg.metrics && deltaLabel(msg.metrics)}
                  <span class="tok-delta" title={deltaTitle(msg.metrics)}>{deltaLabel(msg.metrics)}</span>
                {/if}
              </div>
            </div>
          </div>
        {/each}
        {#if isSending}
          <div class="msg-row">
            <div class="bubble">
              <div class="typing"><span/><span/><span/></div>
              {#if activeThread?.thinking}
                <div class="thinking open">
                  <button class="thinking-head" type="button" on:click={() => toggleThinking(activeThread.thinking)}>
                    <span class="chev">{activeThread.thinking.open ? '▾' : '▸'}</span>
                    <span class="thinking-title">Thinking</span>
                    <span class="thinking-meta">{thinkingSummary(activeThread.thinking)}</span>
                  </button>
                  {#if activeThread.thinking.open}
                    <div class="thinking-body">
                      {#if activeThread.thinking.events.length === 0}
                        <div class="thinking-empty">Waiting for the first runtime event…</div>
                      {:else}
                        {#each activeThread.thinking.events as ev}
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
        disabled={isSending || !activeThread?.agentId}
      ></textarea>
      {#if isSending}
        <button class="send-btn btn-danger" on:click={cancelSend} title="Stop this run">■</button>
      {:else}
        <button class="send-btn btn-primary"
                on:click={send}
                disabled={!activeThread?.agentId || !input.trim()}>
          ↑
        </button>
      {/if}
    </div>
  </div>
</div>

<style>
  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; height: 100%; }
  .page-header { display: flex; align-items: center; justify-content: space-between; flex-shrink: 0; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .controls    { display: flex; gap: .75rem; align-items: center; flex-wrap: wrap; min-width: 0; }

  /* ── voice panel (Story 11) ─────────────────────────────────────── */
  .voice-btn {
    background: #1a1f35; border: 1px solid #2a2f4a; border-radius: 6px;
    padding: .4rem .7rem; cursor: pointer; font-size: .95rem; color: #e8eaf6;
  }
  .voice-btn:hover:not(:disabled) { border-color: #4a5380; }
  .voice-btn:disabled { opacity: .45; cursor: not-allowed; }
  .voice-btn.live { border-color: #e05656; background: #2a1520; animation: voicepulse 1.6s infinite; }
  .voice-btn.connecting { border-color: #c9a227; }
  .voice-btn.error { border-color: #e05656; }
  @keyframes voicepulse { 0%,100% { box-shadow: 0 0 0 0 rgba(224,86,86,.35); } 50% { box-shadow: 0 0 0 5px rgba(224,86,86,0); } }
  .voice-usage {
    font-family: ui-monospace, monospace; font-size: .75rem; color: #8a91b4;
    white-space: nowrap;
  }
  .banner      { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; flex-shrink: 0; }
  .err         { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }

  .threads {
    display: flex;
    gap: .5rem;
    overflow-x: auto;
    padding-bottom: .1rem;
    flex-shrink: 0;
  }
  .thread-chip {
    min-width: 150px;
    max-width: 240px;
    min-height: 42px;
    display: grid;
    grid-template-columns: minmax(0, 1fr) 10px 24px;
    grid-template-rows: auto auto;
    column-gap: .45rem;
    align-items: center;
    padding: .45rem .45rem .45rem .65rem;
    background: #171a2c;
    border: 1px solid #262b48;
    border-radius: 8px;
    color: #c8cadf;
    text-align: left;
    cursor: pointer;
  }
  .thread-chip:hover:not(.active) { background: #1c2036; border-color: #343a5f; }
  .thread-chip.active {
    background: rgba(108, 99, 255, .14);
    border-color: rgba(108, 99, 255, .45);
  }
  .thread-close {
    grid-column: 3;
    grid-row: 1 / span 2;
    background: transparent;
    border: none;
    color: #7f86ab;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 12px;
    width: 20px;
    height: 20px;
    border-radius: 4px;
  }
  .thread-close:hover {
    color: #fff;
    background: rgba(255, 255, 255, 0.1);
  }
  .thread-title,
  .thread-agent {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .thread-title {
    grid-column: 1;
    font-size: .8rem;
    font-weight: 650;
  }
  .thread-agent {
    grid-column: 1;
    color: #7f86ab;
    font-size: .68rem;
  }
  .thread-dot {
    grid-column: 2;
    grid-row: 1 / span 2;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #36d399;
    box-shadow: 0 0 0 3px rgba(54, 211, 153, .12);
  }

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
  .msg-parts { margin-top: 8px; display: flex; flex-direction: column; gap: 8px; }
  .part-image { max-width: 100%; max-height: 360px; border-radius: 8px; align-self: flex-start; }
  .part-audio { width: 100%; max-width: 420px; }
  .part-file { font-size: .85rem; color: var(--accent, #6c8cff); text-decoration: none; }
  .part-file:hover { text-decoration: underline; }
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

  /* ── Branching (Story 8) ─────────────────────────────────────────── */
  .branches { display: flex; gap: .4rem; flex-wrap: wrap; padding: 0 0 .15rem; }
  .branch-chip {
    background: #1c1f35; border: 1px solid #2a2f4a; color: #7b82a8;
    font-size: .72rem; font-family: monospace;
    padding: .2rem .65rem; border-radius: 999px;
  }
  .branch-chip:hover:not(.active) { background: #252840; color: #c8cadf; }
  .branch-chip.active {
    background: rgba(108, 99, 255, .15); border-color: rgba(108, 99, 255, .4);
    color: #8b85ff; cursor: default;
  }
  .bubble { position: relative; }
  .fork-btn {
    position: absolute; top: .25rem; right: .35rem;
    background: none; color: #4d5478; font-size: .8rem;
    padding: .05rem .3rem; border-radius: 5px;
    opacity: 0; transition: opacity .12s;
  }
  .bubble:hover .fork-btn { opacity: 1; }
  .fork-btn:hover:not(:disabled) { background: rgba(108, 99, 255, .18); color: #8b85ff; }

  .bmeta { display: flex; align-items: center; gap: .55rem; flex-wrap: wrap; }
  .tok-delta {
    font-size: .68rem; font-family: monospace; color: #5a5f82;
    cursor: help; white-space: nowrap;
  }
  .tok-delta:hover { color: #8b85ff; }

  /* ── Confirm Modal ─────────────────────────────────────────────── */
  .confirm-modal-backdrop {
    position: fixed; top: 0; left: 0; right: 0; bottom: 0;
    background: rgba(10, 12, 24, 0.8); backdrop-filter: blur(4px);
    z-index: 1000; display: flex; align-items: center; justify-content: center;
  }
  .confirm-modal {
    background: #1c1f35; border: 1px solid #2a2f4a; border-radius: 8px;
    padding: 1.5rem; max-width: 500px; width: 90%;
    box-shadow: 0 10px 30px rgba(0,0,0,0.5);
    display: flex; flex-direction: column; gap: 1rem;
  }
  .confirm-modal h2 { margin: 0; color: #fff; font-size: 1.25rem; }
  .confirm-modal p { margin: 0; color: #a1a7c4; font-size: 0.95rem; }
  .reason-box, .args-box {
    background: #131526; padding: 0.75rem; border-radius: 6px;
    font-size: 0.85rem; color: #c8cadf; border: 1px solid #1a1e36;
  }
  .args-box pre {
    margin: 0.5rem 0 0 0; white-space: pre-wrap; word-wrap: break-word;
    color: #a3d9a5; font-family: monospace; font-size: 0.8rem;
  }
  .confirm-actions {
    display: flex; gap: 0.75rem; justify-content: flex-end; margin-top: 0.5rem;
  }
  .btn-danger { background: rgba(220, 53, 69, 0.2); color: #ff6b81; border: 1px solid rgba(220, 53, 69, 0.4); }
  .btn-danger:hover { background: rgba(220, 53, 69, 0.3); }
</style>
