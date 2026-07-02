// planlanes.js — organizes a Studio workflow into the three fixed lanes of the
// Guided Studio Builder — Trigger, Work Plan, Delivery — and derives the
// plain-English "plan review" shown before save. Pure & unit-tested.
//
// Consumes the workflow model used across Studio: { name, trigger, channels[],
// flow: { nodes[], edges[], entry, output } } and the block helpers.

import { stepLabel, blockReadiness, riskLevel } from './blockmeta.js'

// laneOf assigns a flow node to a lane. Triggers start; exits/deliveries finish;
// everything else is the work plan.
export function laneOf(node) {
  const k = node?.kind || ''
  if (k === 'trigger') return 'trigger'
  if (k === 'exit') return 'delivery'
  return 'work'
}

// Order the work nodes by following edges from the entry (topological-ish), so
// the plan reads start-to-finish. Unreachable nodes are appended in array order.
function orderWork(nodes, edges, entry) {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const out = []
  const seen = new Set()
  const visit = (id) => {
    const n = byId.get(id)
    if (!n || seen.has(id)) return
    seen.add(id)
    if (laneOf(n) === 'work') out.push(n)
    edges.filter((e) => e && e.from === id).forEach((e) => visit(e.to))
  }
  if (entry) visit(entry)
  for (const n of nodes) if (laneOf(n) === 'work' && !seen.has(n.id)) { seen.add(n.id); out.push(n) }
  return out
}

// toLanes returns { trigger, work, delivery } — arrays of "lane cards". Each card
// carries the node plus its computed label, readiness, and risk so the view can
// render without recomputing. Delivery also includes synthetic channel cards.
export function toLanes(workflow) {
  const flow = workflow?.flow || {}
  const nodes = flow.nodes || []
  const edges = flow.edges || []
  const ctx = { edges, entry: flow.entry || '' }
  const card = (node) => ({
    id: node.id,
    node,
    label: stepLabel(node),
    readiness: blockReadiness(node, ctx),
    risk: riskLevel(node),
  })

  const trigger = nodes.filter((n) => laneOf(n) === 'trigger').map(card)
  const work = orderWork(nodes, edges, ctx.entry).map(card)
  const delivery = nodes.filter((n) => laneOf(n) === 'delivery').map(card)

  // A workflow.trigger with no explicit trigger node still belongs in the lane
  // as an implied "how it starts" card.
  if (trigger.length === 0 && workflow?.trigger?.type) {
    trigger.push({
      id: '__trigger__', node: null, implied: true,
      label: triggerLabel(workflow.trigger),
      readiness: { status: 'ready', missing: [], reasons: [] }, risk: 'low',
    })
  }
  // Channels are delivery destinations even without an exit node.
  for (const ch of workflow?.channels || []) {
    delivery.push({
      id: `__chan__${ch}`, node: null, implied: true,
      label: `Send to ${ch}`,
      readiness: { status: 'ready', missing: [], reasons: [] }, risk: 'medium',
    })
  }
  if (delivery.length === 0) {
    delivery.push({
      id: '__console__', node: null, implied: true, label: 'Return the result',
      readiness: { status: 'ready', missing: [], reasons: [] }, risk: 'low',
    })
  }
  return { trigger, work, delivery }
}

// migrateEndpoints converts legacy structural trigger/exit NODES into the
// workflow's trigger + channels settings and removes them from the graph, so
// old flows authored with draggable entry/exit blocks keep working under the
// lane model (Guided Studio Builder, Story 2). Non-destructive: returns a new
// workflow and only migrates when there's something to migrate.
export function migrateEndpoints(workflow) {
  const flow = workflow?.flow
  if (!flow || !Array.isArray(flow.nodes)) return workflow
  const triggerNodes = flow.nodes.filter((n) => n.kind === 'trigger')
  const exitNodes = flow.nodes.filter((n) => n.kind === 'exit')
  if (triggerNodes.length === 0 && exitNodes.length === 0) return workflow

  let trigger = workflow.trigger || null
  let entry = flow.entry || ''
  const channels = Array.isArray(workflow.channels) ? [...workflow.channels] : []
  const removeIds = new Set()

  // First trigger node → workflow.trigger; entry becomes whatever it fed.
  if (triggerNodes.length && !trigger?.type) {
    const t = triggerNodes[0]
    const p = t.params || {}
    trigger = { type: p.kind === 'cron' ? 'schedule' : (p.kind || 'manual'), config: p.config || {} }
    const fed = (flow.edges || []).find((e) => e && e.from === t.id)
    if (fed) entry = fed.to
    removeIds.add(t.id)
  }
  // Exit nodes → a delivery channel (when the route names one) and removed.
  for (const x of exitNodes) {
    const p = x.params || {}
    const chan = p.config?.channel || p.config?.id
    if (p.route === 'channel' && chan && !channels.includes(chan)) channels.push(chan)
    removeIds.add(x.id)
  }

  const nodes = flow.nodes.filter((n) => !removeIds.has(n.id))
  const edges = (flow.edges || []).filter((e) => e && !removeIds.has(e.from) && !removeIds.has(e.to))
  return {
    ...workflow,
    trigger: trigger || workflow.trigger,
    channels,
    flow: { ...flow, nodes, edges, entry },
  }
}

// triggerLabel renders a workflow.trigger into plain English.
export function triggerLabel(trigger) {
  if (!trigger) return 'Manual'
  const t = trigger.type || 'manual'
  if (t === 'schedule' || t === 'cron') {
    const cron = trigger.config?.cron
    return cron ? `On a schedule (${cron})` : 'On a schedule'
  }
  if (t === 'http') return 'On an HTTP request'
  if (t === 'channel') return 'When a message arrives'
  return 'Run manually'
}

// planReview builds the plain-English summary shown before save (slice items
// 6 & 11): when it runs, what it does, what tools/code it uses, where output
// goes, and what needs attention or is risky.
export function planReview(workflow) {
  const { trigger, work, delivery } = toLanes(workflow)
  const usesTools = []
  const usesCode = []
  const risks = []
  const attention = []
  const perms = new Set()

  const SYSTEMISH = /(shell|exec|file|read_file|write_file|fs_|filesystem|os_|system)/i
  const WEBISH = /(http|fetch|url|request|api|browse|search)/i

  for (const c of work) {
    if (c.node?.kind === 'tool' && c.node.tool) {
      usesTools.push(c.label)
      if (SYSTEMISH.test(c.node.tool)) perms.add('Access files or the system')
      if (WEBISH.test(c.node.tool)) perms.add('Make web requests')
    }
    if (c.node?.kind === 'python') { usesCode.push(c.label); perms.add('Run custom code') }
    if (c.node?.kind === 'agent') perms.add('Delegate to another agent')
    if (c.risk === 'high') { risks.push(`${c.label} (external/elevated)`); perms.add('Send data externally') }
    if (c.readiness.status === 'needs-attention') attention.push({ label: c.label, reasons: c.readiness.reasons })
  }
  for (const c of delivery) {
    if (c.risk === 'high' || (c.node?.kind === 'exit' && c.risk !== 'low')) {
      risks.push(`${c.label} (sends externally)`)
      perms.add('Send messages externally')
    }
  }

  return {
    when: trigger[0]?.label || 'Manual',
    does: work.map((c) => c.label),
    usesTools,
    usesCode,
    deliversTo: delivery.map((c) => c.label),
    permissions: Array.from(perms),
    risks,
    attention,
    ready: attention.length === 0,
  }
}
