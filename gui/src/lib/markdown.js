// markdown.js — rich rendering for chat bubbles.
//
// Turns an agent's plain-text reply into formatted, SAFE HTML with:
//   • GFM markdown (bold/italics/lists/tables/links/images)
//   • LaTeX math via KaTeX  ( \(…\) $…$ inline, \[…\] $$…$$ block )
//   • syntax-highlighted fenced code via highlight.js
//   • Mermaid charts/diagrams from ```mermaid / ```xychart fences
//   • interactive data charts (Apache ECharts) from a ```chart JSON fence:
//       ```chart
//       { "xAxis": {"type":"category","data":[...]},
//         "yAxis": {"type":"value"},
//         "series": [{ "type":"line", "data":[...] }] }
//       ```
//     The JSON is an ECharts `option`, parsed with JSON.parse (no eval) so it
//     can't smuggle in code, then restyled to a modern theme before rendering.
//
// Two exports:
//   parseMarkdown(text) -> sanitized HTML string (safe to use with {@html}).
//   richRenderer(node)  -> Svelte action: after the HTML mounts, it highlights
//                          code blocks and replaces mermaid fences with SVG.
//
// SECURITY: agent output is untrusted. marked passes raw HTML through, so every
// parsed string is run through DOMPurify before it touches the DOM, and Mermaid
// runs in securityLevel:'strict' (no embedded HTML / click handlers). The only
// HTML injected without DOMPurify is the SVG WE generate from Mermaid in strict
// mode — never agent-authored markup.
//
// Heavy libs (mermaid, katex, highlight.js) are bundled eagerly by project
// decision; rendering work is deferred to a microtask so a long history doesn't
// block the main thread on mount.

import { marked } from 'marked'
import markedKatex from 'marked-katex-extension'
import DOMPurify from 'dompurify'
import hljs from 'highlight.js'
import mermaid from 'mermaid'
import * as echarts from 'echarts'

import 'katex/dist/katex.min.css'
import 'highlight.js/styles/github-dark.css'
import './markdown.css'

// ── marked configuration (once) ──────────────────────────────────────────────
marked.setOptions({ gfm: true, breaks: true })
marked.use(
  markedKatex({
    throwOnError: false, // a bad equation renders a small error, never crashes the message
    output: 'html', // HTML-only output is simpler + safer to sanitize than MathML
    nonStandard: true, // also accept single-$ inline math
  }),
)

// Force every link to open safely in a new tab (agent links are untrusted).
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A' && node.getAttribute('href')) {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer nofollow')
  }
})

/**
 * Parse markdown (with math) into sanitized HTML safe for `{@html}`.
 * @param {string} text
 * @returns {string} sanitized HTML
 */
export function parseMarkdown(text) {
  if (text == null || text === '') return ''
  let html
  try {
    html = marked.parse(String(text), { async: false })
  } catch (_) {
    // Never let a parse error break the bubble — fall back to escaped text.
    return DOMPurify.sanitize(String(text))
  }
  return DOMPurify.sanitize(html, { ADD_ATTR: ['target', 'rel'] })
}

// ── Mermaid (initialized lazily, once) ───────────────────────────────────────
let mermaidReady = false
function ensureMermaid() {
  if (mermaidReady) return
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: 'strict',
    theme: 'dark',
    fontFamily: 'inherit',
    // Throw on bad diagrams instead of injecting Mermaid's raw "Syntax error in
    // text" graphic — we render our own clean fallback in the catch below.
    suppressErrorRendering: true,
  })
  mermaidReady = true
}

let chartSeq = 0

// ── ECharts theming ──────────────────────────────────────────────────────────
// A premium, "ultra-modern" look inspired by TradingView (smooth area lines with
// a gradient fade + crosshair axis-pointer), Google Sheets (clean rounded bars,
// minimal axes) and ECharts/amCharts-grade polish. We restyle every series so
// charts look intentional regardless of what the agent passed — the data,
// labels, axes structure, and title are preserved; visuals are themed.

// Vibrant palette anchored on the app accent.
const CHART_PALETTE = ['#6c63ff', '#22d3ee', '#34d399', '#fbbf24', '#fb7185', '#a78bfa', '#38bdf8']

// Convert a #hex (3 or 6 digit) to an rgba() string with the given alpha.
function hexA(hex, a) {
  let h = String(hex || '').replace('#', '')
  if (h.length === 3) h = h.split('').map((c) => c + c).join('')
  if (h.length !== 6) return `rgba(108,99,255,${a})`
  const n = parseInt(h, 16)
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`
}

// Vertical ECharts gradient (top → bottom) for area/bar fills.
function vGrad(hex, topA, botA) {
  return new echarts.graphic.LinearGradient(0, 0, 0, 1, [
    { offset: 0, color: hexA(hex, topA) },
    { offset: 1, color: hexA(hex, botA) },
  ])
}

let _theme
function readTheme() {
  if (_theme) return _theme
  const root = getComputedStyle(document.documentElement)
  const v = (n, f) => root.getPropertyValue(n).trim() || f
  _theme = {
    accent: v('--accent', '#6c63ff'),
    text: v('--text', '#e6e9f2'),
    tick: v('--text-muted', '#8b93ab'),
    grid: 'rgba(255,255,255,0.06)',
    bg: v('--bg-elev', '#141927'),
    font: getComputedStyle(document.body).fontFamily || 'inherit',
  }
  return _theme
}

function styleAxis(option, key, t) {
  const apply = (ax) => {
    if (!ax || typeof ax !== 'object') return ax
    ax.axisLine = Object.assign({ show: false }, ax.axisLine)
    ax.axisTick = Object.assign({ show: false }, ax.axisTick)
    ax.axisLabel = Object.assign({ color: t.tick, fontSize: 11 }, ax.axisLabel)
    const isValue = ax.type === 'value' || key === 'yAxis'
    ax.splitLine = Object.assign({ show: isValue, lineStyle: { color: t.grid } }, ax.splitLine)
    return ax
  }
  if (Array.isArray(option[key])) option[key] = option[key].map(apply)
  else option[key] = apply(option[key] || {})
}

function decorateTooltip(tt, t) {
  tt.backgroundColor = tt.backgroundColor || 'rgba(12,15,26,0.94)'
  tt.borderColor = tt.borderColor || hexA(t.accent, 0.45)
  tt.borderWidth = tt.borderWidth ?? 1
  tt.textStyle = Object.assign({ color: t.text }, tt.textStyle)
  tt.padding = tt.padding ?? 12
  tt.extraCssText = tt.extraCssText || 'border-radius:10px;box-shadow:0 8px 24px rgba(0,0,0,0.45);'
}

// Restyle a parsed ECharts option to the modern theme (in place) and return it.
function themeEChartsOption(option) {
  if (!option || typeof option !== 'object') return option
  const t = readTheme()
  option.backgroundColor = 'transparent'
  // Authoritative palette: override whatever colors the agent passed so charts
  // look consistent and on-brand (the model's taste varies wildly).
  option.color = CHART_PALETTE
  option.textStyle = Object.assign({ color: t.tick, fontFamily: t.font }, option.textStyle)
  option.animationDuration = option.animationDuration ?? 800
  option.animationEasing = option.animationEasing || 'cubicOut'
  if (option.title) {
    option.title = Object.assign(
      { left: 'left', textStyle: { color: t.text, fontSize: 15, fontWeight: 600 } },
      option.title,
    )
  }

  const series = Array.isArray(option.series) ? option.series : option.series ? [option.series] : []
  const hasAxis = series.some((s) => s && ['bar', 'line', 'scatter'].includes(s.type))

  if (hasAxis) {
    option.grid = Object.assign({ left: 8, right: 18, top: 40, bottom: 8, containLabel: true }, option.grid)
    styleAxis(option, 'xAxis', t)
    styleAxis(option, 'yAxis', t)
    option.tooltip = Object.assign(
      { trigger: 'axis', axisPointer: { type: 'line', lineStyle: { color: hexA(t.accent, 0.35), type: 'dashed', width: 1 } } },
      option.tooltip,
    )
  } else {
    option.tooltip = Object.assign({ trigger: 'item' }, option.tooltip)
  }
  decorateTooltip(option.tooltip, t)

  const multi = series.length > 1
  const arcOrRadar = series.some((s) => s && (s.type === 'pie' || s.type === 'radar'))
  if (multi || arcOrRadar) {
    option.legend = Object.assign(
      { top: 8, right: 10, itemWidth: 9, itemHeight: 9, textStyle: { color: t.text } },
      option.legend,
    )
    option.legend.icon = 'circle' // force circular markers regardless of agent input
  }

  // Our visual styling is authoritative — it OVERRIDES agent-supplied colors/
  // styles (Object.assign target = agent's object, our props applied on top) so
  // every chart gets the gradient/glow/rounded look. Data, labels, names, and
  // any label formatters the agent set are preserved.
  series.forEach((s, i) => {
    if (!s || typeof s !== 'object') return
    const color = CHART_PALETTE[i % CHART_PALETTE.length]
    if (s.type === 'line') {
      if (s.smooth === undefined) s.smooth = true
      s.symbol = 'circle'
      s.showSymbol = false
      s.lineStyle = Object.assign(s.lineStyle || {}, {
        width: 2.5, color, shadowColor: hexA(color, 0.5), shadowBlur: 12, shadowOffsetY: 6,
      })
      s.itemStyle = Object.assign(s.itemStyle || {}, { color, borderColor: '#fff', borderWidth: 2 })
      s.areaStyle = { color: vGrad(color, 0.35, 0.02) }
      s.emphasis = s.emphasis || { focus: 'series' }
    } else if (s.type === 'bar') {
      s.itemStyle = Object.assign(s.itemStyle || {}, { borderRadius: [6, 6, 0, 0], color: vGrad(color, 0.95, 0.35) })
      s.barMaxWidth = s.barMaxWidth || 46
      s.emphasis = { itemStyle: { color: vGrad(color, 1.0, 0.55) } }
    } else if (s.type === 'pie') {
      if (!s.radius) s.radius = '70%'
      s.itemStyle = Object.assign(s.itemStyle || {}, { borderRadius: 6, borderColor: t.bg, borderWidth: 3 })
      // Strip per-slice colors so the authoritative palette (option.color) wins.
      if (Array.isArray(s.data)) {
        s.data.forEach((d) => {
          if (d && typeof d === 'object' && d.itemStyle) delete d.itemStyle.color
        })
      }
      s.label = Object.assign(s.label || {}, { color: t.tick })
      s.emphasis = s.emphasis || { scale: true, scaleSize: 8, itemStyle: { shadowBlur: 16, shadowColor: 'rgba(0,0,0,0.4)' } }
    } else if (s.type === 'scatter') {
      s.itemStyle = Object.assign(s.itemStyle || {}, { color: hexA(color, 0.8), shadowBlur: 8, shadowColor: hexA(color, 0.4) })
      s.symbolSize = s.symbolSize || 12
    } else if (s.type === 'radar') {
      s.areaStyle = { color: hexA(color, 0.15) }
      s.lineStyle = Object.assign(s.lineStyle || {}, { color, width: 2 })
      s.itemStyle = Object.assign(s.itemStyle || {}, { color })
      s.symbolSize = s.symbolSize || 5
    }
  })

  if (option.radar) {
    option.radar = Object.assign(
      {
        axisName: { color: t.tick },
        splitLine: { lineStyle: { color: t.grid } },
        splitArea: { show: false },
        axisLine: { lineStyle: { color: t.grid } },
      },
      option.radar,
    )
  }
  return option
}

function showBlockError(pre, msg) {
  const note = document.createElement('div')
  note.className = 'mermaid-error' // reuse the error chip styling
  note.textContent = '⚠ ' + msg
  pre.replaceWith(note)
}

// Parse a Mermaid `xychart-beta` block into an ECharts option, so a model that
// insists on emitting Mermaid for data (some coder models do, even via a script)
// still gets a real themed chart instead of a fragile Mermaid render. Returns
// null when the block isn't a usable xychart.
//
// Grammar handled (Mermaid xychart-beta):
//   xychart-beta [horizontal]
//   title "…"
//   x-axis ["a","b",…]        (or a bare/numeric list)
//   y-axis "label" min --> max  (label/range ignored — ECharts autoscales)
//   line  [n, n, …]            (one or more series)
//   bar   [n, n, …]
function xychartToECharts(src) {
  const lines = String(src)
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  let title = ''
  let labels = []
  let horizontal = false
  const series = []
  for (const line of lines) {
    if (/^xychart/i.test(line)) {
      if (/\bhorizontal\b/i.test(line)) horizontal = true
      continue
    }
    let m
    if ((m = line.match(/^title\s+"?(.+?)"?\s*$/i))) {
      title = m[1]
    } else if ((m = line.match(/^x-axis\s+\[(.*)\]\s*$/i))) {
      labels = m[1]
        .split(',')
        .map((t) => t.trim().replace(/^["']|["']$/g, ''))
        .filter((t) => t !== '')
    } else if (/^y-axis\b/i.test(line)) {
      // axis label/range — ECharts handles scaling; ignore.
    } else if ((m = line.match(/^line\s+\[(.*)\]/i))) {
      series.push({ type: 'line', data: xyNums(m[1]) })
    } else if ((m = line.match(/^bar\s+\[(.*)\]/i))) {
      series.push({ type: 'bar', data: xyNums(m[1]) })
    }
  }
  if (!series.length || series.every((s) => s.data.length === 0)) return null

  const cat = { type: 'category', data: labels }
  const val = { type: 'value' }
  const option = {
    series: series.map((s) => ({ type: s.type, data: s.data })),
    // Mermaid "horizontal" swaps the axes.
    xAxis: horizontal ? val : cat,
    yAxis: horizontal ? cat : val,
  }
  if (title) option.title = { text: title }
  return option
}

function xyNums(s) {
  return s
    .split(',')
    .map((t) => parseFloat(t.trim()))
    .filter((n) => !Number.isNaN(n))
}

// addCodeCopyButton adds a hover "Copy" button to a fenced code block's <pre>,
// so users can grab code with one click (Modern Chat Workspace — rich rendering).
function addCodeCopyButton(codeEl) {
  const pre = codeEl.closest('pre')
  if (!pre || pre.dataset.copyBtn) return
  pre.dataset.copyBtn = '1'
  pre.style.position = pre.style.position || 'relative'
  const btn = document.createElement('button')
  btn.type = 'button'
  btn.className = 'code-copy-btn'
  btn.textContent = 'Copy'
  btn.setAttribute('aria-label', 'Copy code')
  btn.addEventListener('click', async (e) => {
    e.preventDefault()
    e.stopPropagation()
    try {
      await navigator.clipboard.writeText(codeEl.textContent || '')
      btn.textContent = 'Copied!'
      setTimeout(() => { btn.textContent = 'Copy' }, 1200)
    } catch (_) { /* clipboard unavailable */ }
  })
  pre.appendChild(btn)
}

/**
 * Svelte action for a container holding parsed markdown. Pass the message text
 * as the parameter (`use:richRenderer={msg.text}`) so it re-runs when the bubble
 * content changes. Highlights code, renders mermaid/xychart fences to SVG, and
 * renders ```chart fences to interactive ECharts charts.
 */
export function richRenderer(node, value = '') {
  // ECharts instances (+ their ResizeObservers) to tear down on re-render/destroy.
  const liveCharts = []
  let lastValue = value == null ? '' : String(value)
  let rendered = false
  let runToken = 0

  function disposeCharts() {
    while (liveCharts.length) {
      const c = liveCharts.pop()
      try {
        c.ro && c.ro.disconnect()
      } catch (_) {
        /* ignore */
      }
      try {
        c.inst && c.inst.dispose()
      } catch (_) {
        /* already gone */
      }
    }
  }

  // Replace an element with a themed, responsive ECharts chart and track it for
  // cleanup. Shared by the ```chart path and the Mermaid-xychart conversion.
  function mountEChart(replaceEl, option) {
    const wrap = document.createElement('div')
    wrap.className = 'chart-canvas'
    const host = document.createElement('div')
    host.className = 'chart-echarts'
    wrap.appendChild(host)
    replaceEl.replaceWith(wrap)
    try {
      const inst = echarts.init(host, null, { renderer: 'canvas' })
      inst.setOption(themeEChartsOption(option))
      let ro
      if (typeof ResizeObserver !== 'undefined') {
        ro = new ResizeObserver(() => inst.resize())
        ro.observe(wrap)
      }
      liveCharts.push({ inst, ro })
    } catch (err) {
      showBlockError(wrap, 'Chart could not be rendered: ' + ((err && err.message) || 'invalid option'))
    }
  }

  function run() {
    disposeCharts()

    // (1) Syntax-highlight fenced code — skip the blocks we render specially.
    node.querySelectorAll('pre code').forEach((el) => {
      const cls = el.className || ''
      if (
        cls.includes('language-mermaid') ||
        cls.includes('language-xychart') ||
        cls.includes('language-chart') ||
        el.dataset.hl
      )
        return
      try {
        hljs.highlightElement(el)
      } catch (_) {
        /* leave the code unhighlighted on failure */
      }
      el.dataset.hl = '1'
      addCodeCopyButton(el)
    })

    // (2) Replace mermaid / xychart fences with rendered SVG diagrams.
    const diagrams = node.querySelectorAll('code.language-mermaid, code.language-xychart')
    if (diagrams.length) {
      ensureMermaid()
      diagrams.forEach(async (codeEl) => {
        const pre = codeEl.closest('pre') || codeEl
        if (pre.dataset.done) return
        pre.dataset.done = '1'
        const src = (codeEl.textContent || '').trim()
        if (!src) return
        // Mermaid is for DIAGRAMS, not data plots. Models sometimes emit an
        // `xychart` to chart data — which is the generate_chart tool's job and
        // also frequently fails to parse. Don't even try: show an on-message note
        // so the user never sees a raw Mermaid error and the intent is clear.
        const isDataChart = (codeEl.className || '').includes('language-xychart') || /^xychart/i.test(src)
        if (isDataChart) {
          // Don't hand data plots to Mermaid — convert to a real ECharts chart so
          // the user always gets a themed chart, even when the model emits Mermaid.
          const option = xychartToECharts(src)
          if (option) {
            mountEChart(pre, option)
          } else {
            showBlockError(pre, 'Data charts are rendered with the generate_chart tool, not Mermaid — ask again and the agent will draw it.')
          }
          return
        }
        try {
          const { svg } = await mermaid.render('sm-chart-' + ++chartSeq, src)
          const wrap = document.createElement('div')
          wrap.className = 'mermaid-chart'
          wrap.innerHTML = svg // trusted: generated by us in strict mode
          pre.replaceWith(wrap)
        } catch (err) {
          showBlockError(pre, 'Diagram could not be rendered: ' + ((err && err.message) || 'invalid mermaid'))
        }
      })
    }

    // (3) Render ```chart JSON fences as interactive ECharts charts.
    node.querySelectorAll('code.language-chart').forEach((codeEl) => {
      const pre = codeEl.closest('pre') || codeEl
      if (pre.dataset.done) return
      pre.dataset.done = '1'
      const raw = (codeEl.textContent || '').trim()
      if (!raw) return
      let option
      try {
        option = JSON.parse(raw) // JSON only — no functions/eval can ride in
      } catch (err) {
        showBlockError(pre, 'Invalid chart JSON: ' + ((err && err.message) || 'parse error'))
        return
      }
      const series = option && option.series
      if (!series || (Array.isArray(series) && series.length === 0)) {
        showBlockError(pre, 'Chart needs a "series".')
        return
      }
      mountEChart(pre, option)
    })
  }

  // Defer to a microtask so Svelte has injected the {@html} content first, and
  // a long message list doesn't block the main thread synchronously on mount.
  // The action replaces fenced chart blocks with live DOM. Svelte can still call
  // update() later for unrelated chat-store changes; if the message text did not
  // change, do NOT dispose the chart, because the original code fence is gone.
  function scheduleRun() {
    const token = ++runToken
    Promise.resolve().then(() => {
      if (token !== runToken) return
      run()
      rendered = true
    })
  }

  scheduleRun()
  return {
    update(nextValue = '') {
      const normalized = nextValue == null ? '' : String(nextValue)
      if (rendered && normalized === lastValue) return
      lastValue = normalized
      scheduleRun()
    },
    destroy() {
      runToken++
      disposeCharts()
    },
  }
}
