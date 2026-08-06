// The remediation seam: Go declares the vocabulary, the client handles it.
//
// This contract spans two languages with nothing between them, and it has
// failed twice here. Studio.svelte's readinessAction shipped handling five
// actions the server never emitted while six the server did emit fell through
// to a no-op — a "Fix this" button that did nothing. The security panel later
// grew a second, parallel vocabulary. Both times each side was internally
// consistent and nothing compared them.
//
// So this reads both files. It is deliberately blunt: regex over source text.
// A parser would be more elegant and would not have caught anything more.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { handledActionIds, actionKind, applyDraftFix, DRAFT_FIXES, SHARED_CHANNELS } from './fixactions.js'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '../../../..')
const read = (p) => readFileSync(resolve(repoRoot, p), 'utf8')

const goVocabulary = read('internal/studio/fixactions.go')
const goSecurity = read('internal/studio/security_preflight.go')

/** Ids and kinds declared in the Go registry table. */
function goActions() {
  const table = goVocabulary.slice(goVocabulary.indexOf('var fixActions = []FixAction{'))
  const body = table.slice(0, table.indexOf('\n}'))
  const consts = Object.fromEntries(
    [...goVocabulary.matchAll(/\t(Fix\w+)\s+=\s+"([^"]+)"/g)].map((m) => [m[1], m[2]]),
  )
  return [...body.matchAll(/\{(Fix\w+),\s*"([^"]*)",\s*(FixKind\w+)\}/g)].map((m) => ({
    id: consts[m[1]],
    label: m[2],
    kind: { FixKindNavigate: 'navigate', FixKindApply: 'apply', FixKindFocus: 'focus' }[m[3]],
  }))
}

describe('fix-action vocabulary parity', () => {
  it('reads a non-trivial vocabulary out of Go', () => {
    const actions = goActions()
    expect(actions.length).toBeGreaterThan(5)
    for (const a of actions) {
      expect(a.id, `an entry failed to resolve its constant: ${JSON.stringify(a)}`).toBeTruthy()
      expect(a.kind).toBeTruthy()
    }
  })

  it('handles every action the server can emit', () => {
    const missing = goActions().map((a) => a.id).filter((id) => !handledActionIds().includes(id))
    expect(missing, `Go declares these but the client has no handler: ${missing.join(', ')}`).toEqual([])
  })

  it('handles nothing the server never declares', () => {
    const declared = goActions().map((a) => a.id)
    const stray = handledActionIds().filter((id) => !declared.includes(id))
    expect(stray, `the client handles ids Go does not declare: ${stray.join(', ')}`).toEqual([])
  })

  it('agrees with Go on what each action DOES', () => {
    // A navigate action wired as a draft edit (or vice versa) is worse than an
    // unhandled one: it silently does the wrong thing.
    const wrong = goActions().filter((a) => actionKind(a.id) !== a.kind)
      .map((a) => `${a.id}: Go says ${a.kind}, client says ${actionKind(a.id) || 'unknown'}`)
    expect(wrong, wrong.join('; ')).toEqual([])
  })

  it('gives every action button text', () => {
    const unlabelled = goActions().filter((a) => !a.label.trim()).map((a) => a.id)
    expect(unlabelled, `an action with no label renders no button: ${unlabelled.join(', ')}`).toEqual([])
  })

  it('keeps the security findings on the shared vocabulary', () => {
    // The security panel used to declare its own ids. Aliases are fine; a
    // second independent list is not.
    expect(goSecurity).not.toMatch(/SecurityFix\w+\s+=\s+"/)
  })
})

describe('applying a draft fix', () => {
  it('drops shared channels and keeps the agent reachable', () => {
    const { draft, message } = applyDraftFix({ channels: ['telegram', 'slack', 'http'] }, 'restrict_to_internal_channels')
    expect(draft.channels).toEqual(['http'])
    expect(message).toContain('telegram')
  })

  it('adds http when removing the shared channels would leave nothing', () => {
    const { draft } = applyDraftFix({ channels: ['telegram'] }, 'restrict_to_internal_channels')
    expect(draft.channels).toEqual(['http'])
  })

  it('reports a no-op instead of pretending to change something', () => {
    const res = applyDraftFix({ channels: ['http'] }, 'restrict_to_internal_channels')
    expect(res.draft).toBeUndefined()
    expect(res.message).toMatch(/already/i)
  })

  it('sets the intent gate without discarding other security settings', () => {
    const { draft } = applyDraftFix({ security: { passphrase: 'hunter2' } }, 'set_intent_gate_deny')
    expect(draft.security).toEqual({ passphrase: 'hunter2', intent_gate: 'deny' })
  })

  it('never mutates the draft it was given', () => {
    const original = { channels: ['telegram', 'http'], security: {} }
    const snapshot = JSON.stringify(original)
    applyDraftFix(original, 'restrict_to_internal_channels')
    applyDraftFix(original, 'set_intent_gate_deny')
    expect(JSON.stringify(original)).toBe(snapshot)
  })

  it('throws on an unknown action rather than doing nothing', () => {
    expect(() => applyDraftFix({}, 'set_something_else')).toThrow(/no draft fix/)
  })

  it('covers every shared channel the security review can name', () => {
    // The two lists are separate by necessity (different languages) — if the Go
    // side learns a new shared channel, this fix must learn it too or the
    // button will leave that channel in place while claiming otherwise.
    const goList = [...read('internal/studio/security_preflight.go')
      .matchAll(/"(telegram|discord|slack|teams|googlechat|whatsapp|whatsapp_web|email|webhook)"/g)]
      .map((m) => m[1])
    for (const c of new Set(goList)) {
      expect(SHARED_CHANNELS, `security review knows "${c}" but the fix does not`).toContain(c)
    }
  })

  it('exposes each fix as a plain function', () => {
    for (const [id, fn] of Object.entries(DRAFT_FIXES)) {
      expect(typeof fn, `${id} should be callable`).toBe('function')
    }
  })
})
