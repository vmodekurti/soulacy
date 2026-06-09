<script>
  import { Handle, Position } from '@xyflow/svelte'
  // A workflow step. Colour & kind chip keyed by node.kind (tool/agent/branch).
  export let data
  $: node = data.node
</script>

<div class="studio-node" class:entry={data.isEntry} style="--node-accent: {data.color}">
  <Handle type="target" position={Position.Left} />
  <div class="node-head">
    <span class="kind-chip">{data.kindLabel}</span>
    {#if data.isEntry}<span class="entry-chip">entry</span>{/if}
  </div>
  <div class="node-title" title={data.label}>{data.label}</div>
  <div class="node-meta">
    <span class="node-id">{node.id}</span>
    {#if node.output}<span class="node-out">→ {node.output}</span>{/if}
  </div>
  <Handle type="source" position={Position.Right} />
</div>

<style>
  .studio-node {
    min-width: 170px;
    max-width: 220px;
    background: var(--bg-elev-2, #1b2235);
    border: 1px solid var(--node-accent);
    border-left: 4px solid var(--node-accent);
    border-radius: 10px;
    padding: 10px 12px;
    color: var(--text, #e6e9f2);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.35);
  }
  .studio-node.entry {
    box-shadow: 0 0 0 2px var(--node-accent), 0 4px 16px rgba(0, 0, 0, 0.4);
  }
  .node-head {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 6px;
  }
  .kind-chip {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    background: var(--node-accent);
    color: #0c0f1a;
    padding: 1px 7px;
    border-radius: 999px;
    font-weight: 700;
  }
  .entry-chip {
    font-size: 10px;
    color: var(--node-accent);
    border: 1px solid var(--node-accent);
    border-radius: 999px;
    padding: 0 6px;
  }
  .node-title {
    font-size: 13px;
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .node-meta {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    margin-top: 4px;
    font-size: 10px;
    color: var(--text-muted, #8b93ab);
  }
  .node-out {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
