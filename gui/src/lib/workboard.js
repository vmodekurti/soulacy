// Workboard column model shared by Workboard.svelte and its tests.

/** Kanban columns in board order. Mirrors internal/workboard statuses. */
export const STATUSES = ['todo', 'running', 'needs_review', 'done', 'failed']

export const STATUS_LABELS = {
  todo:         'Todo',
  running:      'Running',
  needs_review: 'Needs Review',
  done:         'Done',
  failed:       'Failed',
}

/**
 * Returns the status one column to the left (dir=-1) or right (dir=+1),
 * or null when the move would fall off the board or status is unknown.
 */
export function adjacentStatus(status, dir) {
  const i = STATUSES.indexOf(status)
  if (i === -1) return null
  const j = i + dir
  if (j < 0 || j >= STATUSES.length) return null
  return STATUSES[j]
}

/**
 * Whether a task can be (re)run right now: it needs an assigned agent and
 * must not already be running. The server is the final authority (409 on
 * duplicate concurrent runs) — this only drives button state.
 */
export function canRun(task) {
  if (!task) return false
  return Boolean(task.agent_id) && task.status !== 'running'
}

/** Label for the run action: Retry after a failure, Run otherwise. */
export function runLabel(task) {
  return task && task.status === 'failed' ? 'Retry' : 'Run'
}

/** Groups a task list into { status: [tasks] } with every column present. */
export function groupByStatus(tasks) {
  const cols = {}
  for (const s of STATUSES) cols[s] = []
  for (const t of tasks || []) {
    if (cols[t.status]) cols[t.status].push(t)
  }
  return cols
}
