// pyerror.js — turn raw Python tracebacks/errors from a tested block into a
// plain-English explanation with a suggested fix (Guided Studio Builder, Story
// 6 "Python errors shown with plain-English explanations and suggested fixes").
// Pure & unit-tested.

const RULES = [
  {
    match: /KeyError:\s*['"]?([^'"\n]+)/,
    explain: (m) => `The code tried to read a field called “${m[1]}” from its input, but that field wasn't there.`,
    fix: (m) => `Use inputs.get("${m[1]}") instead of inputs["${m[1]}"], or make sure the upstream step produces “${m[1]}”.`,
  },
  {
    match: /NameError:\s*name ['"]([^'"]+)['"] is not defined/,
    explain: (m) => `The code used “${m[1]}” before it was defined (a typo or a missing import).`,
    fix: (m) => `Define “${m[1]}” first, or import the module it comes from.`,
  },
  {
    match: /IndentationError|TabError/,
    explain: () => 'The code has inconsistent indentation.',
    fix: () => 'Use 4 spaces per level and don’t mix tabs with spaces.',
  },
  {
    match: /SyntaxError:\s*(.+)/,
    explain: (m) => `Python couldn't parse the code: ${m[1].trim()}.`,
    fix: () => 'Check for a missing colon, bracket, or quote near the reported line.',
  },
  {
    match: /TypeError:\s*(.+)/,
    explain: (m) => `A value was the wrong type for the operation: ${m[1].trim()}.`,
    fix: () => 'Convert the value first (e.g. int(x), str(x)) or check it isn’t None.',
  },
  {
    match: /ZeroDivisionError/,
    explain: () => 'The code divided by zero.',
    fix: () => 'Guard the division, e.g. `x / y if y else 0`.',
  },
  {
    match: /ModuleNotFoundError:\s*No module named ['"]([^'"]+)['"]/,
    explain: (m) => `The code imports “${m[1]}”, which isn't available in the sandbox.`,
    fix: (m) => `Remove the dependency on “${m[1]}”, or use only the standard library.`,
  },
  {
    match: /JSONDecodeError|Expecting value/,
    explain: () => 'The input wasn’t valid JSON when the code tried to read it.',
    fix: () => 'Make sure the upstream step outputs JSON, or parse defensively.',
  },
  {
    match: /timed out|deadline exceeded|timeout/i,
    explain: () => 'The step ran longer than its time limit and was stopped.',
    fix: () => 'Reduce the work, or raise the step’s timeout in its settings.',
  },
]

// explainPythonError returns { summary, fix } for a raw error/traceback string,
// or a generic fallback when no rule matches.
export function explainPythonError(raw) {
  const text = String(raw || '')
  if (!text.trim()) return { summary: 'The step failed without an error message.', fix: 'Run it again, or add a print() to see what happened.' }
  for (const rule of RULES) {
    const m = text.match(rule.match)
    if (m) return { summary: rule.explain(m), fix: rule.fix(m) }
  }
  // Last line of a traceback is usually the most informative.
  const lines = text.trim().split('\n').filter(Boolean)
  return { summary: lines[lines.length - 1] || 'The step failed.', fix: 'Check the code around the reported line.' }
}
