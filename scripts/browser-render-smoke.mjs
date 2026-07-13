#!/usr/bin/env node
import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawn } from 'node:child_process'

async function loadPlaywright() {
  try {
    return await import('playwright')
  } catch (err) {
    console.log('skip browser render smoke: playwright is not installed')
    process.exit(77)
  }
}

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms))
}

async function waitFor(url, apiKey) {
  for (let i = 0; i < 80; i += 1) {
    try {
      const res = await fetch(`${url}/api/v1/health`, { headers: { Authorization: `Bearer ${apiKey}` } })
      if (res.ok) return
    } catch {
      // keep waiting
    }
    await sleep(250)
  }
  throw new Error(`gateway did not become healthy at ${url}`)
}

const root = new URL('..', import.meta.url).pathname
const bin = process.env.SOULACY_BROWSER_RENDER_BIN || join(root, 'bin', 'soulacy')
const host = process.env.SOULACY_BROWSER_RENDER_HOST || '127.0.0.1'
const port = process.env.SOULACY_BROWSER_RENDER_PORT || '18894'
const apiKey = process.env.SOULACY_BROWSER_RENDER_API_KEY || 'sy_browser_render_smoke'
const baseURL = `http://${host}:${port}`
const workspace = process.env.SOULACY_BROWSER_RENDER_WORKSPACE || mkdtempSync(join(tmpdir(), 'soulacy-browser-render-'))
const outDir = process.env.SOULACY_BROWSER_RENDER_OUT || join(workspace, 'screenshots')
mkdirSync(join(workspace, 'agents'), { recursive: true })
mkdirSync(join(workspace, 'logs'), { recursive: true })
mkdirSync(outDir, { recursive: true })

writeFileSync(join(workspace, 'config.yaml'), `server:
  host: "${host}"
  port: ${port}
  api_key: "${apiKey}"
  gui_enabled: true
llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      model: "llama3.2"
agent_dirs:
  - "${workspace}/agents"
log:
  file: "${workspace}/logs/soulacy.log"
`)

const child = spawn(bin, ['serve'], {
  cwd: root,
  env: {
    ...process.env,
    SOULACY_WORKSPACE: workspace,
    SOULACY_CONFIG_PATH: join(workspace, 'config.yaml'),
  },
  stdio: ['ignore', 'pipe', 'pipe'],
})

let stderr = ''
child.stderr.on('data', chunk => {
  stderr += chunk.toString()
})

let browser
try {
  await waitFor(baseURL, apiKey)
  const { chromium } = await loadPlaywright()
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  const consoleErrors = []
  page.on('console', msg => {
    if (msg.type() === 'error') consoleErrors.push(msg.text())
  })
  page.on('pageerror', err => {
    consoleErrors.push(err.message)
  })

  const routes = [
    ['dashboard', '/'],
    ['studio', '/#studio'],
    ['agents', '/#agents'],
    ['chat', '/#chat'],
    ['channels', '/#channels'],
    ['knowledge', '/#knowledge'],
    ['schedule', '/#schedule'],
    ['providers', '/#providers'],
    ['activity', '/#activity'],
    ['skills', '/#skills'],
    ['config', '/#config'],
  ]

  for (const [name, path] of routes) {
    consoleErrors.length = 0
    await page.goto(`${baseURL}${path}`, { waitUntil: 'networkidle', timeout: 30000 })
    await page.screenshot({ path: join(outDir, `${name}.png`), fullPage: true })
    const text = await page.locator('body').innerText({ timeout: 10000 })
    if (!text || text.length < 20) {
      throw new Error(`${name} rendered too little text`)
    }
    const serious = consoleErrors.filter(line =>
      !/favicon|ResizeObserver loop|Failed to load resource: the server responded with a status of 404/i.test(line)
    )
    if (serious.length) {
      throw new Error(`${name} console error: ${serious[0]}`)
    }
  }
  console.log(`browser render smoke passed; screenshots: ${outDir}`)
} finally {
  if (browser) await browser.close()
  child.kill('SIGTERM')
  await new Promise(resolve => {
    child.once('exit', resolve)
    setTimeout(resolve, 1500)
  })
}

if (stderr.includes('panic:') || stderr.includes('fatal error:')) {
  throw new Error(`gateway emitted fatal stderr: ${stderr.slice(-1000)}`)
}
