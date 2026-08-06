// fixactions.js — the client half of the remediation vocabulary.
//
// The Go half is internal/studio/fixactions.go. Every id it can emit must be
// handled here; every id handled here must exist there. fixactions.test.js
// reads both files and fails the build in either direction, because this seam
// has drifted twice already: Studio.svelte once handled five actions the server
// never sent while six it did send fell through to a no-op, and the security
// panel later grew a second vocabulary of its own.
//
// Everything here is pure. Applying a fix returns a NEW draft and a sentence
// describing what changed; it never touches the DOM, navigates, or reads a
// store. That keeps the interesting half — what the button actually does to
// your workflow — testable without mounting a 10k-line component.

/** Actions that leave Studio for the screen that owns the setting. */
export const NAVIGATE_TARGETS = {
  open_providers: '#providers',
  open_mcp: '#mcp',
  open_delivery: '#channels',
  open_secrets: '#secrets',
}

/** Actions the host handles in place: focus a node, open the bench, etc. */
export const FOCUS_ACTIONS = [
  'choose_model',
  'add_assertions',
  'run_live',
  'open_studio',
  'open_preflight',
  'reveal_node',
]

// Channels that reach people outside the install. Kept in step with
// sharedExternalChannel in internal/studio/security_preflight.go.
export const SHARED_CHANNELS = [
  'telegram', 'discord', 'slack', 'teams', 'googlechat',
  'whatsapp', 'whatsapp_web', 'email', 'webhook',
]

const isShared = (c) => SHARED_CHANNELS.includes(String(c || '').toLowerCase())

/**
 * Fixes Studio applies to the draft itself.
 * Each returns { draft, message } on success, or { message } with no draft when
 * there was nothing to do — the caller reports that rather than silently
 * "succeeding" on a no-op.
 */
export const DRAFT_FIXES = {
  restrict_to_internal_channels(draft) {
    const before = Array.isArray(draft.channels) ? draft.channels : []
    const removed = before.filter(isShared)
    if (!removed.length) {
      return { message: 'This agent is already on internal channels only.' }
    }
    const kept = before.filter((c) => !isShared(c))
    // Leave it reachable. An agent bound to nothing at all is a different kind
    // of broken from an agent bound to too much.
    if (!kept.includes('http')) kept.push('http')
    return {
      draft: { ...draft, channels: kept },
      message: `Removed ${removed.join(', ')} — this agent is now on internal HTTP only.`,
    }
  },

  set_intent_gate_deny(draft) {
    if ((draft.security || {}).intent_gate === 'deny') {
      return { message: 'The intent gate is already set to deny.' }
    }
    return {
      draft: { ...draft, security: { ...(draft.security || {}), intent_gate: 'deny' } },
      message: 'Intent gate set to deny — injection-steered tool calls will be refused.',
    }
  },
}

/** Every id this module knows how to handle. */
export function handledActionIds() {
  return [
    ...Object.keys(NAVIGATE_TARGETS),
    ...FOCUS_ACTIONS,
    ...Object.keys(DRAFT_FIXES),
  ].sort()
}

/** What kind of thing an id is, or '' when it is not in the vocabulary. */
export function actionKind(id) {
  if (!id) return ''
  if (NAVIGATE_TARGETS[id]) return 'navigate'
  if (DRAFT_FIXES[id]) return 'apply'
  if (FOCUS_ACTIONS.includes(id)) return 'focus'
  return ''
}

/**
 * Apply a draft-editing fix. Returns { draft, message } — `draft` is undefined
 * when nothing changed. Throws for an unknown id, because a caller silently
 * dropping one is exactly the failure this module exists to prevent.
 */
export function applyDraftFix(draft, action) {
  const fn = DRAFT_FIXES[action]
  if (!fn) throw new Error(`no draft fix for action "${action}"`)
  if (!draft) return { message: 'There is no draft to change yet.' }
  return fn(draft)
}

/** The button text, when the server did not send one. */
export function fallbackLabel(action) {
  return actionKind(action) ? 'Fix this' : ''
}
