/*
 * Studio plugin UI — M1-A (Story S1.x).
 *
 * Runs inside a sandboxed iframe (sandbox="allow-scripts allow-forms", NO
 * allow-same-origin) embedded by the Soulacy Svelte shell. Because there is no
 * same-origin, cookies and localStorage are unavailable, and — importantly —
 * the scoped plugin token is DEFAULT-DENIED by the capability system on the
 * catalog reads (/api/v1/agents, /tool-catalog, /providers). Direct fetches
 * therefore 403.
 *
 * Instead of broadening plugin permissions, we use a HOST-MEDIATED RPC bridge:
 * the host frame (PluginFrame.svelte), which holds the user's authenticated
 * session, performs those reads for us and relays the result over postMessage.
 *
 * postMessage contract (must match PluginFrame.svelte exactly):
 *   iframe -> host:  { source: 'studio',      type: 'catalog.request',  id }
 *   host -> iframe:  { source: 'studio-host', type: 'catalog.response', id,
 *                      ok: true,  data: { agents, tools, providers } }
 *                  | { source: 'studio-host', type: 'catalog.response', id,
 *                      ok: false, error: '<msg>' }
 *
 * The `data` payload mirrors the raw API shapes:
 *   data.agents    = GET /api/v1/agents       → { agents: [...], count }
 *   data.tools     = GET /api/v1/tool-catalog → { python_tools, mcp_tools, builtins }
 *   data.providers = GET /api/v1/providers    → { providers: {...}, default_provider }
 *
 * The token is still delivered in the URL fragment and kept only in a closure
 * variable; it is used solely for the (optional) direct-fetch fallback below.
 */
(function () {
  'use strict';

  // ---------------------------------------------------------------------------
  // Token: read from location.hash, then scrub it from the address so it never
  // lingers in any logged/visible URL. Kept in a closure variable only.
  // ---------------------------------------------------------------------------
  function readToken() {
    var hash = window.location.hash || '';
    if (hash.charAt(0) === '#') hash = hash.slice(1);
    var token = '';
    hash.split('&').forEach(function (pair) {
      var eq = pair.indexOf('=');
      if (eq < 0) return;
      var k = pair.slice(0, eq);
      var v = pair.slice(eq + 1);
      if (k === 'token') token = decodeURIComponent(v);
    });
    return token;
  }

  var TOKEN = readToken();

  try {
    if (window.location.hash) {
      window.history.replaceState(null, '', window.location.pathname + window.location.search);
    }
  } catch (_) {
    /* sandbox may block history; harmless */
  }

  // ---------------------------------------------------------------------------
  // Tiny DOM helpers (no framework).
  // ---------------------------------------------------------------------------
  function $(id) {
    return document.getElementById(id);
  }

  function setCount(id, n) {
    var el = $(id);
    if (el) el.textContent = String(n);
  }

  function clear(el) {
    while (el && el.firstChild) el.removeChild(el.firstChild);
  }

  // Render a list of {label, sub} items into a <ul>. Empty/error states render
  // a single muted row instead of leaving the group blank.
  function renderList(listId, items, opts) {
    opts = opts || {};
    var ul = $(listId);
    if (!ul) return;
    clear(ul);

    if (opts.error) {
      var errLi = document.createElement('li');
      errLi.className = 'item item-error';
      errLi.textContent = opts.error;
      ul.appendChild(errLi);
      return;
    }
    if (!items || items.length === 0) {
      var emptyLi = document.createElement('li');
      emptyLi.className = 'item item-empty';
      emptyLi.textContent = opts.emptyText || 'None available';
      ul.appendChild(emptyLi);
      return;
    }

    items.forEach(function (it) {
      var li = document.createElement('li');
      li.className = 'item';
      // draggable visual affordance for M1 (no drop target yet)
      li.setAttribute('draggable', 'true');

      var label = document.createElement('span');
      label.className = 'item-label';
      label.textContent = it.label;
      li.appendChild(label);

      if (it.sub) {
        var sub = document.createElement('span');
        sub.className = 'item-sub';
        sub.textContent = it.sub;
        li.appendChild(sub);
      }
      ul.appendChild(li);
    });
  }

  // ---------------------------------------------------------------------------
  // Renderers — map each raw API response shape into palette rows. Shared by
  // both the host-bridge path and the direct-fetch fallback.
  // ---------------------------------------------------------------------------

  // { agents: [{ name, description, ... }], count }
  function renderAgents(data) {
    var agents = (data && data.agents) || [];
    var items = agents.map(function (a) {
      return { label: a.name || a.id || 'agent', sub: a.description || '' };
    });
    setCount('count-agents', items.length);
    renderList('list-agents', items, { emptyText: 'No agents' });
    return items.length;
  }

  // { python_tools:[{name,...}], mcp_tools:[{name,full_name,server,...}],
  //   builtins:[{name,...}] }
  function renderTools(data) {
    var py = (data && data.python_tools) || [];
    var mcp = (data && data.mcp_tools) || [];
    var builtins = (data && data.builtins) || [];
    var items = [];
    builtins.forEach(function (t) {
      items.push({ label: t.name, sub: 'builtin' });
    });
    py.forEach(function (t) {
      items.push({ label: t.name, sub: 'python' });
    });
    mcp.forEach(function (t) {
      items.push({ label: t.name || t.full_name, sub: 'mcp · ' + (t.server || '') });
    });
    setCount('count-tools', items.length);
    renderList('list-tools', items, { emptyText: 'No tools' });
    return items.length;
  }

  // { providers: { <name>: {model, ...} }, default_provider }
  function renderProviders(data) {
    var providers = (data && data.providers) || {};
    var def = (data && data.default_provider) || '';
    var items = Object.keys(providers).map(function (name) {
      var p = providers[name] || {};
      var parts = [];
      if (p.model) parts.push(p.model);
      if (name === def) parts.push('default');
      return { label: name, sub: parts.join(' · ') };
    });
    setCount('count-providers', items.length);
    renderList('list-providers', items, { emptyText: 'No providers' });
    return items.length;
  }

  function renderError(message) {
    setCount('count-agents', 0);
    setCount('count-tools', 0);
    setCount('count-providers', 0);
    renderList('list-agents', null, { error: message });
    renderList('list-tools', null, { error: message });
    renderList('list-providers', null, { error: message });
  }

  function applyCatalog(data) {
    renderAgents(data && data.agents);
    renderTools(data && data.tools);
    renderProviders(data && data.providers);
  }

  function setStatus(text, cls) {
    var status = $('palette-status');
    if (!status) return;
    status.textContent = text;
    if (cls) status.classList.add(cls);
  }

  // ---------------------------------------------------------------------------
  // Host-mediated RPC bridge (primary path).
  // ---------------------------------------------------------------------------
  var REQUEST_ID = 'catalog-' + Date.now() + '-' + Math.random().toString(36).slice(2);
  var HOST_TIMEOUT_MS = 4000;

  function requestCatalogViaHost() {
    return new Promise(function (resolve, reject) {
      if (window.parent === window) {
        reject(new Error('no host frame'));
        return;
      }

      var settled = false;
      var timer = null;

      function cleanup() {
        window.removeEventListener('message', onMessage);
        if (timer) clearTimeout(timer);
      }

      function onMessage(event) {
        // Only trust replies from our own host (the parent window) that match
        // our protocol and correlation id. The host has the user's session;
        // the parent is the only window we asked.
        if (event.source !== window.parent) return;
        var msg = event.data;
        if (!msg || typeof msg !== 'object') return;
        if (msg.source !== 'studio-host' || msg.type !== 'catalog.response') return;
        if (msg.id !== REQUEST_ID) return;

        if (settled) return;
        settled = true;
        cleanup();

        if (msg.ok) {
          resolve(msg.data || {});
        } else {
          reject(new Error(msg.error || 'host catalog request failed'));
        }
      }

      window.addEventListener('message', onMessage);

      timer = setTimeout(function () {
        if (settled) return;
        settled = true;
        cleanup();
        reject(new Error('host did not respond'));
      }, HOST_TIMEOUT_MS);

      // Ask the host. The host verifies event.source === our iframe window, so
      // targetOrigin '*' is safe here (request carries no secret).
      window.parent.postMessage(
        { source: 'studio', type: 'catalog.request', id: REQUEST_ID },
        '*',
      );
    });
  }

  // ---------------------------------------------------------------------------
  // Direct-fetch fallback. Only used if the host bridge is absent/unresponsive
  // (e.g. opened standalone). Expected to 403 under the plugin token today, so
  // it renders a single quiet status rather than noisy per-row errors.
  // ---------------------------------------------------------------------------
  function fetchJson(path) {
    var headers = { Accept: 'application/json' };
    if (TOKEN) headers.Authorization = 'Bearer ' + TOKEN;
    return fetch(path, { headers: headers, credentials: 'omit' }).then(function (res) {
      if (!res.ok) throw new Error('HTTP ' + res.status);
      return res.json();
    });
  }

  function loadViaDirectFetch() {
    return Promise.allSettled([
      fetchJson('/api/v1/agents').then(renderAgents),
      fetchJson('/api/v1/tool-catalog').then(renderTools),
      fetchJson('/api/v1/providers').then(renderProviders),
    ]).then(function (results) {
      var ok = results.filter(function (r) { return r.status === 'fulfilled'; }).length;
      if (ok === results.length) {
        setStatus('Capabilities loaded (direct).', 'status-ok');
      } else if (ok === 0) {
        renderError('Unavailable');
        setStatus('Capability reads unavailable — host bridge did not respond.', 'status-error');
      } else {
        setStatus('Loaded ' + ok + ' of ' + results.length + ' capability groups (direct).', 'status-warn');
      }
    });
  }

  // ---------------------------------------------------------------------------
  // Boot. Prefer the host bridge; fall back to direct fetch on timeout/absence.
  // ---------------------------------------------------------------------------
  function boot() {
    setStatus('Loading capabilities…');

    requestCatalogViaHost().then(
      function (data) {
        applyCatalog(data);
        setStatus('Capabilities loaded.', 'status-ok');
      },
      function (hostErr) {
        // Host bridge unavailable — try the direct path as a graceful fallback.
        loadViaDirectFetch().catch(function () {
          renderError('Unavailable');
          setStatus('Could not load capabilities: ' + hostErr.message, 'status-error');
        });
      },
    );
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
