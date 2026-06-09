/*
 * Map a compiled Studio workflow into @xyflow/svelte nodes & edges.
 *
 * Compile contract:
 *   workflow = {
 *     name, trigger:{type,config}, channels:[],
 *     flow:{ nodes:[{id,kind,tool,agent,input,output,x,y,params}],
 *            edges:[{from,to,if}], entry }
 *   }
 *
 * Mapping rules:
 *   - flow.nodes -> xyflow nodes. Position from node.x/node.y; when both are
 *     missing/zero, fall back to a simple left-to-right layered auto-layout
 *     (BFS depth from `entry` = column, ordinal-within-depth = row).
 *   - flow.edges -> xyflow edges (from->source, to->target). Edges whose `to`
 *     is "end"/"" are skipped (they only mark a terminal, not a real link).
 *   - trigger renders as a dedicated START node feeding `entry`.
 *   - channels render as a dedicated OUTPUT node fed by terminal nodes
 *     (nodes with no outgoing real edge, or whose edge.to is end/"").
 *   - node colour/label keyed by kind (tool/agent/branch).
 */

const KIND_META = {
  tool:   { color: '#2bb3a3', label: 'Tool' },
  agent:  { color: '#6c63ff', label: 'Agent' },
  branch: { color: '#f5a742', label: 'Branch' },
}

const COL_W = 240
const ROW_H = 120
const X0 = 60
const Y0 = 140

const TERMINAL = new Set(['end', ''])

export function kindMeta(kind) {
  return KIND_META[kind] || { color: '#8b93ab', label: kind || 'node' }
}

function needsAutoLayout(nodes) {
  // If every node has the same (or zero) coordinates, lay out automatically.
  return nodes.every((n) => (!n.x && !n.y))
}

// BFS depth from entry → column index. Disconnected nodes get appended after
// the deepest reached column so nothing overlaps.
function layeredPositions(nodes, edges, entry) {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const adj = new Map(nodes.map((n) => [n.id, []]))
  edges.forEach((e) => {
    if (TERMINAL.has(e.to)) return
    if (adj.has(e.from) && byId.has(e.to)) adj.get(e.from).push(e.to)
  })

  const depth = new Map()
  const start = entry && byId.has(entry) ? entry : (nodes[0] && nodes[0].id)
  if (start) {
    const q = [start]
    depth.set(start, 0)
    while (q.length) {
      const cur = q.shift()
      for (const nx of adj.get(cur) || []) {
        if (!depth.has(nx)) {
          depth.set(nx, depth.get(cur) + 1)
          q.push(nx)
        }
      }
    }
  }

  let maxDepth = 0
  depth.forEach((d) => { if (d > maxDepth) maxDepth = d })
  // Place unreached nodes in trailing columns.
  nodes.forEach((n) => {
    if (!depth.has(n.id)) depth.set(n.id, ++maxDepth)
  })

  // Row index = order within a column.
  const rowOf = new Map()
  const counts = new Map()
  nodes.forEach((n) => {
    const d = depth.get(n.id)
    const r = counts.get(d) || 0
    rowOf.set(n.id, r)
    counts.set(d, r + 1)
  })

  const pos = new Map()
  nodes.forEach((n) => {
    pos.set(n.id, {
      x: X0 + depth.get(n.id) * COL_W,
      y: Y0 + rowOf.get(n.id) * ROW_H,
    })
  })
  return pos
}

/**
 * @returns {{ nodes: Array, edges: Array }} xyflow-ready arrays.
 */
export function toFlow(workflow) {
  const flow = (workflow && workflow.flow) || {}
  const rawNodes = Array.isArray(flow.nodes) ? flow.nodes : []
  const rawEdges = Array.isArray(flow.edges) ? flow.edges : []
  const entry = flow.entry || (rawNodes[0] && rawNodes[0].id) || ''

  const auto = needsAutoLayout(rawNodes)
  const positions = auto ? layeredPositions(rawNodes, rawEdges, entry) : null

  const nodes = rawNodes.map((n) => {
    const meta = kindMeta(n.kind)
    const position = auto
      ? (positions.get(n.id) || { x: X0, y: Y0 })
      : { x: n.x || 0, y: n.y || 0 }
    return {
      id: n.id,
      type: 'studio',
      position,
      data: {
        node: n,
        label: n.tool || n.agent || n.id,
        kindLabel: meta.label,
        color: meta.color,
        isEntry: n.id === entry,
      },
    }
  })

  const edges = []
  const ids = new Set(rawNodes.map((n) => n.id))
  // Real flow edges (skip terminal markers).
  rawEdges.forEach((e, i) => {
    if (TERMINAL.has(e.to)) return
    if (!ids.has(e.from) || !ids.has(e.to)) return
    edges.push({
      id: 'e-' + e.from + '-' + e.to + '-' + i,
      source: e.from,
      target: e.to,
      label: e.if || undefined,
      animated: false,
    })
  })

  // ── Trigger as a START node → entry ──────────────────────────────────────
  const trigger = (workflow && workflow.trigger) || null
  if (trigger && trigger.type && entry && ids.has(entry)) {
    const triggerY = (positions && positions.get(entry)) ? positions.get(entry).y
      : (nodes.find((x) => x.id === entry)?.position.y ?? Y0)
    nodes.unshift({
      id: '__trigger__',
      type: 'studioTrigger',
      position: { x: X0 - COL_W, y: triggerY },
      data: { label: trigger.type, config: trigger.config || {} },
      selectable: false,
    })
    edges.push({
      id: 'e-trigger-' + entry,
      source: '__trigger__',
      target: entry,
      animated: true,
    })
  }

  // ── Channels as an OUTPUT node fed by terminal nodes ─────────────────────
  const channels = (workflow && Array.isArray(workflow.channels)) ? workflow.channels : []
  if (channels.length) {
    // Terminal = node with no outgoing real edge (or edge.to is end/"").
    const hasRealOut = new Set()
    rawEdges.forEach((e) => { if (!TERMINAL.has(e.to)) hasRealOut.add(e.from) })
    const terminals = rawNodes.filter((n) => !hasRealOut.has(n.id))

    let maxX = X0
    nodes.forEach((nd) => { if (nd.position.x > maxX) maxX = nd.position.x })
    const avgY = terminals.length
      ? terminals.reduce((s, n) => {
          const nd = nodes.find((x) => x.id === n.id)
          return s + (nd ? nd.position.y : Y0)
        }, 0) / terminals.length
      : Y0

    nodes.push({
      id: '__output__',
      type: 'studioOutput',
      position: { x: maxX + COL_W, y: avgY },
      data: { channels },
      selectable: false,
    })
    terminals.forEach((t, i) => {
      edges.push({
        id: 'e-' + t.id + '-output-' + i,
        source: t.id,
        target: '__output__',
        animated: true,
      })
    })
  }

  return { nodes, edges }
}
