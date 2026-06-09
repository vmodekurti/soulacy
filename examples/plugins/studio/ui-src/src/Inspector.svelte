<script>
  // Right-hand inspector.
  //   - When a flow node is selected: read-only view of its fields (Wave 1).
  //   - Otherwise (no node selected): editable workflow FRAMING — the trigger
  //     and the output channel(s). Edits are written back into the in-memory
  //     draft via onChange so subsequent Test/Save use the edited values, and
  //     the canvas re-renders the START / SINK chips.
  export let node = null        // raw flow node | null
  export let workflow = null    // the in-memory draft { trigger, channels, ... }
  export let channels = []      // [{ id, name }] from the catalog
  export let onChange = () => {} // (patch) => void; patch merged into workflow

  const TRIGGER_TYPES = ['schedule', 'channel', 'webhook', 'manual']
  const CRON_HINTS = [
    '0 8 * * 1-5  =  weekdays 8am',
    '*/15 * * * *  =  every 15 minutes',
    '0 0 * * 0  =  Sundays at midnight',
  ]

  function entries(params) {
    if (!params || typeof params !== 'object') return []
    return Object.entries(params)
  }
  function fmt(v) {
    if (v == null) return ''
    if (typeof v === 'object') return JSON.stringify(v, null, 2)
    return String(v)
  }

  // ── Trigger editing ───────────────────────────────────────────────────────
  $: trigger = (workflow && workflow.trigger) || { type: '', config: {} }
  $: triggerType = trigger.type || ''
  $: triggerCron = (trigger.config && trigger.config.cron) || ''
  $: triggerChannel = (trigger.config && trigger.config.channel) || ''

  function setTriggerType(t) {
    // Reset config to the shape the new type needs (keep nothing stale).
    onChange({ trigger: { type: t, config: {} } })
  }
  function setTriggerCron(cron) {
    onChange({ trigger: { type: 'schedule', config: { ...(trigger.config || {}), cron } } })
  }
  function setTriggerChannel(ch) {
    onChange({ trigger: { type: 'channel', config: { ...(trigger.config || {}), channel: ch } } })
  }

  // ── Output channel(s) editing (multi) ─────────────────────────────────────
  // workflow.channels is an array of channel ids (strings) or {type/name} objs;
  // we normalise to a Set of selected ids for the multi-picker.
  function channelKey(ch) {
    if (typeof ch === 'string') return ch
    return (ch && (ch.id || ch.type || ch.name)) || ''
  }
  $: selectedChannels = new Set(
    ((workflow && Array.isArray(workflow.channels)) ? workflow.channels : []).map(channelKey),
  )
  function toggleOutputChannel(id, on) {
    const next = new Set(selectedChannels)
    if (on) next.add(id)
    else next.delete(id)
    onChange({ channels: Array.from(next) })
  }
</script>

<aside class="inspector" aria-label="Node inspector">
  <h2 class="insp-title">Inspector</h2>

  {#if node}
    <!-- Selected node: read-only fields (unchanged Wave 1 behaviour). -->
    <dl class="fields">
      <dt>id</dt><dd>{node.id}</dd>
      <dt>kind</dt><dd>{node.kind || '—'}</dd>
      {#if node.tool}<dt>tool</dt><dd>{node.tool}</dd>{/if}
      {#if node.agent}<dt>agent</dt><dd>{node.agent}</dd>{/if}
      <dt>input</dt><dd>{fmt(node.input) || '—'}</dd>
      <dt>output</dt><dd>{fmt(node.output) || '—'}</dd>
    </dl>

    <h3 class="sub">params</h3>
    {#if entries(node.params).length === 0}
      <p class="insp-empty">No params.</p>
    {:else}
      <dl class="fields">
        {#each entries(node.params) as [k, v]}
          <dt>{k}</dt><dd><pre>{fmt(v)}</pre></dd>
        {/each}
      </dl>
    {/if}
  {:else if workflow}
    <!-- No node selected: edit the workflow framing (trigger + output). -->
    <p class="insp-hint">Editing how this workflow starts and where it sends output. Select a node to inspect it.</p>

    <section class="frame">
      <h3 class="sub">Trigger</h3>
      <label class="field-label" for="trigger-type">type</label>
      <select
        id="trigger-type"
        value={triggerType}
        on:change={(e) => setTriggerType(e.target.value)}
      >
        <option value="" disabled selected={!triggerType}>Choose…</option>
        {#each TRIGGER_TYPES as t}
          <option value={t} selected={t === triggerType}>{t}</option>
        {/each}
      </select>

      {#if triggerType === 'schedule'}
        <label class="field-label" for="trigger-cron">cron</label>
        <input
          id="trigger-cron"
          type="text"
          placeholder="0 8 * * 1-5"
          value={triggerCron}
          on:input={(e) => setTriggerCron(e.target.value)}
        />
        <ul class="hints">
          {#each CRON_HINTS as h}<li>{h}</li>{/each}
        </ul>
      {:else if triggerType === 'channel'}
        <label class="field-label" for="trigger-channel">channel</label>
        <select
          id="trigger-channel"
          value={triggerChannel}
          on:change={(e) => setTriggerChannel(e.target.value)}
        >
          <option value="" disabled selected={!triggerChannel}>Choose channel…</option>
          {#each channels as ch}
            <option value={ch.id} selected={ch.id === triggerChannel}>{ch.name || ch.id}</option>
          {/each}
        </select>
        {#if !channels.length}<p class="insp-empty">No channels in catalog.</p>{/if}
      {:else if triggerType === 'webhook'}
        <p class="insp-hint">Fires when an inbound webhook is received.</p>
      {:else if triggerType === 'manual'}
        <p class="insp-hint">Runs only when triggered by hand.</p>
      {/if}
    </section>

    <section class="frame">
      <h3 class="sub">Output channels</h3>
      {#if !channels.length}
        <p class="insp-empty">No channels in catalog.</p>
      {:else}
        <ul class="checklist">
          {#each channels as ch}
            <li>
              <label>
                <input
                  type="checkbox"
                  checked={selectedChannels.has(ch.id)}
                  on:change={(e) => toggleOutputChannel(ch.id, e.target.checked)}
                />
                <span>{ch.name || ch.id}</span>
              </label>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {:else}
    <p class="insp-empty">Generate a workflow to edit its trigger and output.</p>
  {/if}
</aside>

<style>
  .inspector {
    flex: 0 0 280px;
    background: var(--bg-elev);
    border-left: 1px solid var(--border);
    padding: 16px;
    overflow-y: auto;
  }
  .insp-title {
    margin: 0 0 12px;
    font-size: 13px;
    text-transform: uppercase;
    letter-spacing: 1px;
    color: var(--text-muted);
  }
  .insp-empty { font-size: 12px; color: var(--text-muted); }
  .insp-hint { font-size: 12px; color: var(--text-muted); margin: 0 0 12px; }
  .sub {
    margin: 18px 0 8px;
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
  }
  .frame { border-top: 1px solid var(--border); padding-top: 4px; margin-top: 6px; }
  .field-label {
    display: block;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
    margin: 10px 0 4px;
  }
  select, input[type='text'] {
    width: 100%;
    padding: 7px 10px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--text);
    font-size: 13px;
    outline: none;
  }
  select:focus, input[type='text']:focus { border-color: var(--accent); }
  .hints {
    list-style: none;
    margin: 6px 0 0;
    padding: 0;
    font-family: ui-monospace, monospace;
    font-size: 11px;
    color: var(--text-muted);
  }
  .hints li { margin: 2px 0; }
  .checklist { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .checklist label { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text); cursor: pointer; }
  .checklist input[type='checkbox'] { accent-color: var(--accent); }
  .fields { margin: 0; }
  .fields dt {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
    margin-top: 10px;
  }
  .fields dd {
    margin: 2px 0 0;
    font-size: 13px;
    color: var(--text);
    word-break: break-word;
  }
  .fields pre {
    margin: 0;
    white-space: pre-wrap;
    font-family: ui-monospace, monospace;
    font-size: 12px;
    background: var(--bg-elev-2);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 6px 8px;
  }
</style>
