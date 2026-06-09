/*
 * Host-mediated RPC bridge client (Studio M1 Wave 2).
 *
 * The Studio UI runs inside a sandboxed iframe with NO same-origin, so its
 * scoped plugin token is default-denied on gateway reads. Instead of fetching
 * directly, every privileged op is relayed through the HOST frame
 * (gui/src/pages/PluginFrame.svelte), which holds the user's authenticated
 * session and answers only a fixed whitelist.
 *
 * postMessage contract (must match PluginFrame.svelte EXACTLY):
 *   iframe -> host:  { source: 'studio', type: <op>.request, id, ...payload }
 *   host -> iframe:  { source: 'studio-host', type: <op>.response, id,
 *                      ok: true,  data: {...} }
 *                  | { source: 'studio-host', type: <op>.response, id,
 *                      ok: false, error: '<msg>' }
 *
 * Whitelisted ops:
 *   catalog  -> { agents, tools, providers, channels }       (Wave 1 + M2)
 *   compile  payload { intent, answers? } -> { workflow, questions, notes }
 *   test     payload { workflow, input, mocks?:{<nodeId>:<output>},
 *                      assertions?:[{target,op,value}], mode?:"dry" }
 *              -> { trace:[{nodeId,kind,input,output,mocked?}], result,
 *                   assertions:[{target,op,value,pass,detail}], passed,
 *                   mode, warnings? }                                 (M5)
 *   plan     payload { workflow }         -> { tier, reasons[], requiresConsent,
 *                                              consentItems[{kind,name,reason}] }   (M2)
 *   validate payload { workflow }         -> { ok, errors[{nodeId?,edgeIndex?,message}],
 *                                              warnings[{nodeId?,message}] }         (M3)
 *   save     payload { workflow, acceptPrivilegedExposure? }
 *              -> { agentId, enabled }
 *               | rejects Error with .requiresConsent + .consentItems (409 fallback)
 *   discover payload { query, kind? } -> { results:[...], count }            (M4)
 *              relays GET /registries/search; packages passed through verbatim.
 *   install  payload { source, checksum?, name? }
 *              -> { staged, multiStep:true, preview, security?, note }       (M4)
 *              relays POST /plugins/install (stage). Staging is a real,
 *              consent-bearing step; activation still needs an Approve in the
 *              Plugins page. Reply is honest about that (multiStep:true).
 */

const HOST_TIMEOUT_MS = 8000

function newId(op) {
  return op + '-' + Date.now() + '-' + Math.random().toString(36).slice(2)
}

/**
 * Send one request to the host and resolve with its `data` (or reject on
 * error/timeout/absent host). Each call installs a one-shot, id-correlated,
 * source-checked listener.
 *
 * @param {string} op       e.g. 'catalog', 'compile', 'test', 'save'
 * @param {object} payload  extra fields merged into the request message
 * @param {number} [timeoutMs]
 */
export function bridgeRequest(op, payload = {}, timeoutMs = HOST_TIMEOUT_MS) {
  const reqType = op + '.request'
  const resType = op + '.response'
  const id = newId(op)

  return new Promise((resolve, reject) => {
    if (window.parent === window) {
      reject(new Error('no host frame (Studio must run embedded in the portal)'))
      return
    }

    let settled = false
    let timer = null

    function cleanup() {
      window.removeEventListener('message', onMessage)
      if (timer) clearTimeout(timer)
    }

    function onMessage(event) {
      // Trust ONLY replies from our own host (the parent window) that match the
      // protocol and our correlation id.
      if (event.source !== window.parent) return
      const msg = event.data
      if (!msg || typeof msg !== 'object') return
      if (msg.source !== 'studio-host' || msg.type !== resType) return
      if (msg.id !== id) return
      if (settled) return
      settled = true
      cleanup()
      if (msg.ok) resolve(msg.data || {})
      else {
        // Surface structured fields (e.g. the consent 409 fallback) on the
        // rejection so callers can react without re-parsing a string.
        const err = new Error(msg.error || (op + ' request failed'))
        if (msg.requiresConsent != null) err.requiresConsent = msg.requiresConsent
        if (msg.consentItems != null) err.consentItems = msg.consentItems
        reject(err)
      }
    }

    window.addEventListener('message', onMessage)

    timer = setTimeout(() => {
      if (settled) return
      settled = true
      cleanup()
      reject(new Error('host did not respond (' + op + ')'))
    }, timeoutMs)

    // Host verifies event.source === this iframe window, so targetOrigin '*'
    // is safe (requests carry no secret).
    window.parent.postMessage({ source: 'studio', type: reqType, id, ...payload }, '*')
  })
}

export const bridge = {
  catalog: () => bridgeRequest('catalog'),
  compile: (intent, answers) => bridgeRequest('compile', { intent, answers }),
  // M5: a test bench. `opts` may carry { mocks, assertions, mode }; only
  // present fields are sent so the backend defaults the rest.
  test: (workflow, input, opts = {}) =>
    bridgeRequest('test', {
      workflow,
      input,
      ...(opts.mocks ? { mocks: opts.mocks } : {}),
      ...(opts.assertions ? { assertions: opts.assertions } : {}),
      ...(opts.mode ? { mode: opts.mode } : {}),
    }),
  plan: (workflow) => bridgeRequest('plan', { workflow }),
  validate: (workflow) => bridgeRequest('validate', { workflow }),
  save: (workflow, acceptPrivilegedExposure) =>
    bridgeRequest('save', { workflow, acceptPrivilegedExposure }),
  // M4: discover installable capabilities and stage an install. `install` may
  // take a little longer (the host stages + runs a safety pass), so give it a
  // wider timeout than the default catalog/compile ops.
  discover: (query, kind) => bridgeRequest('discover', { query, kind }),
  install: ({ source, checksum, name } = {}) =>
    bridgeRequest('install', { source, checksum, name }, 30000),
}
