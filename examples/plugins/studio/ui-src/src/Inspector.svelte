<script>
  // Right-hand inspector: read-only view of a selected node's fields.
  export let node = null   // raw flow node {id,kind,tool,agent,input,output,params} | null

  function entries(params) {
    if (!params || typeof params !== 'object') return []
    return Object.entries(params)
  }
  function fmt(v) {
    if (v == null) return ''
    if (typeof v === 'object') return JSON.stringify(v, null, 2)
    return String(v)
  }
</script>

<aside class="inspector" aria-label="Node inspector">
  <h2 class="insp-title">Inspector</h2>
  {#if !node}
    <p class="insp-empty">Select a node to inspect its fields.</p>
  {:else}
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
  .sub {
    margin: 18px 0 8px;
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
  }
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
