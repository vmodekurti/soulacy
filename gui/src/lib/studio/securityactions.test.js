// The security-fix seam: Go emits action ids, Svelte handles them.
//
// This is a hand-maintained contract across two languages with nothing between
// them, and it has already failed once in this codebase. Studio.svelte's
// readinessAction shipped handling five actions the server never emitted, while
// six the server did emit fell through to a no-op — so a blocker rendered a
// "Fix this" button that did nothing at all, and no test noticed because each
// side was internally consistent.
//
// So this test reads both sides and compares them. It is deliberately blunt:
// regex over source text. A parser would be more elegant and would not have
// caught anything more.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(here, '../../../..')

const goSource = readFileSync(resolve(repoRoot, 'internal/studio/security_preflight.go'), 'utf8')
const svelteSource = readFileSync(resolve(repoRoot, 'gui/src/pages/Studio.svelte'), 'utf8')

/** Ids the Go side declares as appliable (its SecurityFixActions constants). */
function declaredActions() {
  const consts = [...goSource.matchAll(/SecurityFix\w+\s*=\s*"([^"]+)"/g)].map((m) => m[1])
  return [...new Set(consts)].sort()
}

/** Ids the Svelte applySecurityFix switch actually handles. */
function handledActions() {
  const fn = svelteSource.slice(svelteSource.indexOf('function applySecurityFix'))
  const body = fn.slice(0, fn.indexOf('\n  function applySecurityRecommendation'))
  return [...new Set([...body.matchAll(/case '([^']+)':/g)].map((m) => m[1]))].sort()
}

describe('security fix actions', () => {
  it('declares at least one appliable fix', () => {
    expect(declaredActions().length).toBeGreaterThan(0)
  })

  it('every action the server can emit is handled by the client', () => {
    const missing = declaredActions().filter((a) => !handledActions().includes(a))
    expect(missing, `Go emits these but Studio.svelte has no case for them: ${missing.join(', ')}`).toEqual([])
  })

  it('the client handles nothing the server never sends', () => {
    const stray = handledActions().filter((a) => !declaredActions().includes(a))
    expect(stray, `Studio.svelte handles actions no longer emitted by Go: ${stray.join(', ')}`).toEqual([])
  })

  it('every appliable finding carries a button label', () => {
    // An Action with no ActionLabel renders no button, so the fix is
    // unreachable — the same dead end as having no action at all.
    const findingsWithAction = [...goSource.matchAll(/Action:\s+(SecurityFix\w+),\s*\n\s*ActionLabel:\s*"([^"]+)"/g)]
    expect(findingsWithAction.length).toBe(declaredActions().length)
    for (const [, , label] of findingsWithAction) {
      expect(label.length, 'button labels should read as an action').toBeGreaterThan(4)
    }
  })

  it('keeps the written fix even when a button exists', () => {
    // The button is an accelerator, not a replacement for saying what it does.
    // A finding with an action but no prose leaves the user clicking blind.
    const actionBlocks = goSource.split('SecurityFinding{').slice(1)
    for (const block of actionBlocks) {
      const body = block.slice(0, block.indexOf('\n\t\t})'))
      if (body.includes('Action:')) {
        expect(body, 'a finding with a button must still explain the fix').toContain('Fix:')
      }
    }
  })
})
