import { describe, it, expect } from 'vitest'
import { autoConnectEdge, explainConnection, wouldCreateCycle } from './autoconnect.js'
import { inferPython, suggestPythonSteps } from './pyinfer.js'
import { explainPythonError } from './pyerror.js'

describe('autoConnectEdge', () => {
  it('connects the newest prior work node to the new node', () => {
    const nodes = [{ id: 'a', kind: 'tool' }, { id: 'b', kind: 'python' }]
    expect(autoConnectEdge(nodes, { id: 'c', kind: 'tool' })).toEqual({ from: 'b', to: 'c' })
  })
  it('skips structural nodes and returns null for the first step', () => {
    expect(autoConnectEdge([{ id: 't', kind: 'trigger' }], { id: 'a', kind: 'tool' })).toBeNull()
    expect(autoConnectEdge([], { id: 'a', kind: 'tool' })).toBeNull()
  })
  it('does not duplicate an existing edge', () => {
    const nodes = [{ id: 'a', kind: 'tool' }]
    expect(autoConnectEdge(nodes, { id: 'b', kind: 'tool' }, [{ from: 'a', to: 'b' }])).toBeNull()
  })
})

describe('explainConnection', () => {
  it('accepts a normal connection', () => {
    expect(explainConnection({ id: 'a', kind: 'tool' }, { id: 'b', kind: 'tool' })).toBeNull()
  })
  it('rejects self-loops, post-exit, and pre-trigger', () => {
    expect(explainConnection({ id: 'a' }, { id: 'a' })).toMatch(/itself/)
    expect(explainConnection({ id: 'a', kind: 'exit' }, { id: 'b', kind: 'tool' })).toMatch(/delivery/)
    expect(explainConnection({ id: 'a', kind: 'tool' }, { id: 'b', kind: 'trigger' })).toMatch(/trigger/)
  })
})

describe('wouldCreateCycle', () => {
  it('detects a back-edge', () => {
    const edges = [{ from: 'a', to: 'b' }, { from: 'b', to: 'c' }]
    expect(wouldCreateCycle(edges, 'c', 'a')).toBe(true)
    expect(wouldCreateCycle(edges, 'a', 'c')).toBe(false)
  })
})

describe('inferPython (JS mirror)', () => {
  it('matches computation intents and skips plain ones', () => {
    expect(inferPython('Rank the top stocks').needsPython).toBe(true)
    expect(inferPython('Clean the spreadsheet').template).toBe('clean_csv')
    expect(inferPython('Summarize the news and send to Telegram').needsPython).toBe(false)
  })
})

describe('suggestPythonSteps', () => {
  it('suggests python for computational work nodes only', () => {
    const wf = { flow: { nodes: [
      { id: 'a', kind: 'tool', description: 'Rank stocks by momentum' },
      { id: 'b', kind: 'tool', description: 'Send Telegram message' },
      { id: 'c', kind: 'python', description: 'Clean data' },
    ] } }
    const s = suggestPythonSteps(wf)
    expect(s.map((x) => x.nodeId)).toEqual(['a'])
    expect(s[0].reason).toMatch(/ranking/)
  })
})

describe('explainPythonError', () => {
  it('explains common errors with a fix', () => {
    expect(explainPythonError('KeyError: "price"').summary).toMatch(/price/)
    expect(explainPythonError('KeyError: "price"').fix).toMatch(/inputs.get/)
    expect(explainPythonError("ModuleNotFoundError: No module named 'pandas'").summary).toMatch(/pandas/)
    expect(explainPythonError('ZeroDivisionError: division by zero').summary).toMatch(/zero/)
  })
  it('falls back to the last traceback line', () => {
    expect(explainPythonError('Traceback...\nWeirdError: something odd').summary).toMatch(/something odd/)
    expect(explainPythonError('').summary).toMatch(/without an error/)
  })
})
