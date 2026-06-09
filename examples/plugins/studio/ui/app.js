/*
 * Studio plugin UI — M0 scaffold (Story S0.1).
 *
 * Runs inside a sandboxed iframe (sandbox="allow-scripts allow-forms", NO
 * allow-same-origin) embedded by the Soulacy Svelte shell. Because there is no
 * same-origin, cookies and localStorage are unavailable — the scoped plugin
 * token is delivered in the URL FRAGMENT as `#token=splg_...` and must live
 * only in a JS variable for the lifetime of the page.
 *
 * The token authenticates this page as the `plugin:studio` principal. It is
 * sent as `Authorization: Bearer <token>` on every API call.
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
    // hash may be `token=...` or `a=b&token=...`
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

  // Strip the token from the visible URL fragment (best-effort; ignored if the
  // sandbox forbids history access).
  try {
    if (window.location.hash) {
      window.history.replaceState(null, '', window.location.pathname + window.location.search);
    }
  } catch (_) {
    /* sandbox may block history; harmless */
  }

  // ---------------------------------------------------------------------------
  // API helper. All capability endpoints live under /api/v1 and require the
  // Bearer token. Returns parsed JSON or throws an Error with a useful message.
  // ---------------------------------------------------------------------------
  function api(path) {
    var headers = { Accept: 'application/json' };
    if (TOKEN) headers.Authorization = 'Bearer ' + TOKEN;
    return fetch(path, { headers: headers, credentials: 'omit' }).then(function (res) {
      if (!res.ok) {
        // 403 is the expected outcome today: the gateway plugin route gate
        // (pluginRoutePolicy) does not yet admit plugin tokens to these reads.
        var hint = res.status === 403 ? ' (plugin token not yet permitted on this route)' : '';
        throw new Error('HTTP ' + res.status + hint);
      }
      return res.json();
    });
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
  // Loaders — one per capability endpoint. Each maps the host response shape
  // (verified against internal/gateway handlers) into palette rows.
  // ---------------------------------------------------------------------------

  // GET /api/v1/agents → { agents: [{ name, description, ... }], count }
  function loadAgents() {
    return api('/api/v1/agents').then(
      function (data) {
        var agents = (data && data.agents) || [];
        var items = agents.map(function (a) {
          return { label: a.name || a.id || 'agent', sub: a.description || '' };
        });
        setCount('count-agents', items.length);
        renderList('list-agents', items, { emptyText: 'No agents' });
        return items.length;
      },
      function (err) {
        setCount('count-agents', 0);
        renderList('list-agents', null, { error: err.message });
        throw err;
      }
    );
  }

  // GET /api/v1/tool-catalog → { python_tools:[{name,path,description}],
  //   mcp_tools:[{full_name,name,server,description}], builtins:[{name,description}] }
  function loadTools() {
    return api('/api/v1/tool-catalog').then(
      function (data) {
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
      },
      function (err) {
        setCount('count-tools', 0);
        renderList('list-tools', null, { error: err.message });
        throw err;
      }
    );
  }

  // GET /api/v1/providers → { providers: { <name>: {model, registered, ...} },
  //   default_provider, known: [...], registered: [...] }
  function loadProviders() {
    return api('/api/v1/providers').then(
      function (data) {
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
      },
      function (err) {
        setCount('count-providers', 0);
        renderList('list-providers', null, { error: err.message });
        throw err;
      }
    );
  }

  // ---------------------------------------------------------------------------
  // Boot.
  // ---------------------------------------------------------------------------
  function boot() {
    var status = $('palette-status');

    if (!TOKEN) {
      if (status) {
        status.textContent = 'No plugin token in URL fragment — open Studio from the portal nav.';
        status.classList.add('status-error');
      }
      return;
    }

    // Fire all three reads; settle independently so one 403 doesn't blank the
    // others.
    Promise.allSettled([loadAgents(), loadTools(), loadProviders()]).then(function (results) {
      if (!status) return;
      var ok = results.filter(function (r) {
        return r.status === 'fulfilled';
      }).length;
      if (ok === results.length) {
        status.textContent = 'Capabilities loaded.';
        status.classList.add('status-ok');
      } else if (ok === 0) {
        status.textContent =
          'Capability reads denied (HTTP 403). The gateway plugin gate does not ' +
          'yet permit plugin tokens on these routes — see README.';
        status.classList.add('status-error');
      } else {
        status.textContent = 'Loaded ' + ok + ' of ' + results.length + ' capability groups.';
        status.classList.add('status-warn');
      }
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
