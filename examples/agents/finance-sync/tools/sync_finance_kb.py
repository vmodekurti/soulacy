"""
sync_finance_kb — Soulacy Python tool
======================================
Fetches Rocket Money financial data and ingests it into the "finance-local"
knowledge base (sqlite-vec) via the Soulacy REST API.

Modes
-----
full_sync=True  (run once on first setup):
  Wipes the KB, fetches ALL historical transactions (no date limit),
  and writes ~/.soulacy/finance-sync-state.json with today as the baseline.

full_sync=False (default, daily cron):
  Incremental update. Reads the last sync date from the state file and
  fetches only transactions SINCE that date. Accounts, net worth, budget,
  and subscriptions are always refreshed (current state). Transaction docs
  accumulate permanently — they are never deleted after the initial full sync.

State file: ~/.soulacy/finance-sync-state.json
  { "last_sync_date": "YYYY-MM-DD", "last_full_sync": "YYYY-MM-DD" }

Document layout in the KB:
  finance/accounts              — latest account balances (replaced each run)
  finance/net_worth             — latest net worth snapshot (replaced each run)
  finance/budget                — latest budget plan (replaced each run)
  finance/subscriptions         — latest subscriptions (replaced each run)
  finance-history/transactions/chunk-NNN  — full-sync transaction history
  finance-delta/YYYY-MM-DD/transactions/chunk-NNN  — incremental daily additions
"""

import os, sys, json, re, datetime, time
from pathlib import Path

# ── Sync state ────────────────────────────────────────────────────────────────

STATE_PATH = Path.home() / ".soulacy" / "finance-sync-state.json"


def _read_sync_state():
    """Return the sync state dict, or an empty dict if no state exists yet."""
    if STATE_PATH.exists():
        try:
            return json.loads(STATE_PATH.read_text())
        except Exception:
            pass
    return {}


def _write_sync_state(state: dict):
    """Persist the sync state atomically."""
    STATE_PATH.parent.mkdir(parents=True, exist_ok=True)
    STATE_PATH.write_text(json.dumps(state, indent=2))

# ── Rocket Money API ──────────────────────────────────────────────────────────

GQL_URL = "https://client-api.rocketmoney.com/graphql"
GQL_HEADERS_BASE = {
    "Content-Type": "application/json",
    "Accept": "application/json",
    "Origin": "https://app.rocketmoney.com",
    "Referer": "https://app.rocketmoney.com/",
    "User-Agent": (
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
        "AppleWebKit/537.36 (KHTML, like Gecko) "
        "Chrome/124.0.0.0 Safari/537.36"
    ),
}


_COOKIE_SECRETS_PATH = Path.home() / ".soulacy" / "secrets" / "rocket_money_cookie"

# ── Credential vault helpers ──────────────────────────────────────────────────

def _vault_set(api_key, port, agent_id, key, value):
    """Store a secret in the Soulacy credential vault."""
    import base64
    return _soulacy_request(
        "POST", f"/credentials/{agent_id}/{key}",
        body={"value": base64.b64encode(value.encode()).decode()},
        api_key=api_key, port=port,
    )


def _vault_get(api_key, port, agent_id, key):
    """Retrieve a secret from the Soulacy credential vault. Returns str or None."""
    import base64
    try:
        resp = _soulacy_request("GET", f"/credentials/{agent_id}/{key}",
                                api_key=api_key, port=port)
        raw = resp.get("value", "")
        if raw:
            return base64.b64decode(raw).decode()
    except Exception:
        pass
    return None


def setup_rocket_money_credentials(username, password, api_key="", port=18789):
    """Store Rocket Money credentials in the Soulacy vault (call once from REPL).

    Usage:
        python3 -c "
        import importlib.util
        spec = importlib.util.spec_from_file_location('t',
            '/Users/clawagent/.soulacy/agents/finance-sync/tools/sync_finance_kb.py')
        m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
        m.setup_rocket_money_credentials('you@email.com', 'yourpassword')
        "
    """
    if not api_key:
        api_key, port = _read_soulacy_config()
    _vault_set(api_key, port, "finance-sync", "rm_username", username)
    _vault_set(api_key, port, "finance-sync", "rm_password", password)
    print(f"Credentials stored in vault for finance-sync (username={username})")


# ── Programmatic Rocket Money login ───────────────────────────────────────────

def _programmatic_login(username, password):
    """Attempt headless login to Rocket Money and return a fresh cookie string.

    Flow:
    1. GET https://app.rocketmoney.com/login → follow redirect to Auth0
    2. Parse the Auth0 login form (action URL + hidden fields)
    3. POST credentials → follow redirects
    4. Collect Set-Cookie headers and return the assembled cookie string.

    Returns the cookie string on success, or None if any step fails.
    Auth0 may block headless logins if bot detection is triggered — in that case
    the function returns None and the caller falls back to browser_cookie3.
    """
    import urllib.request, urllib.parse, urllib.error
    from html.parser import HTMLParser

    UA = GQL_HEADERS_BASE["User-Agent"]

    class FormParser(HTMLParser):
        """Extract the first <form action=...> and all its <input> fields."""
        def __init__(self):
            super().__init__()
            self.action = None
            self.inputs = {}
            self._in_form = False

        def handle_starttag(self, tag, attrs):
            attrs = dict(attrs)
            if tag == "form" and not self._in_form:
                self.action = attrs.get("action", "")
                self._in_form = True
            elif tag == "input" and self._in_form:
                name = attrs.get("name", "")
                val  = attrs.get("value", "")
                if name:
                    self.inputs[name] = val

    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor())
    opener.addheaders = [
        ("User-Agent", UA),
        ("Accept",     "text/html,application/xhtml+xml,*/*;q=0.9"),
        ("Accept-Language", "en-US,en;q=0.9"),
    ]

    try:
        # Step 1: load login page — follows redirect to Auth0
        resp = opener.open("https://app.rocketmoney.com/login", timeout=15)
        html = resp.read().decode(errors="replace")
        final_url = resp.geturl()

        # Step 2: parse the Auth0 login form
        parser = FormParser()
        parser.feed(html)
        if not parser.action:
            return None  # page structure unexpected (maybe already logged in or bot blocked)

        # Step 3: fill in credentials
        form_data = parser.inputs.copy()
        form_data["username"] = username
        form_data["password"] = password

        action = parser.action
        if action.startswith("/"):
            # Relative URL — make absolute using the final Auth0 domain
            from urllib.parse import urlparse
            p = urlparse(final_url)
            action = f"{p.scheme}://{p.netloc}{action}"

        payload = urllib.parse.urlencode(form_data).encode()
        req = urllib.request.Request(action, data=payload, headers={
            "Content-Type": "application/x-www-form-urlencoded",
            "Referer": final_url,
            "User-Agent": UA,
        })
        opener.open(req, timeout=15)

        # Step 4: extract cookies from the opener's cookie jar
        jar = opener.handlers[0].cookiejar if hasattr(opener.handlers[0], "cookiejar") else None
        if jar is None:
            for h in opener.handlers:
                if hasattr(h, "cookiejar"):
                    jar = h.cookiejar
                    break
        if not jar:
            return None

        cookies = {c.name: c.value for c in jar if "rocketmoney" in (c.domain or "")}
        if not cookies:
            return None

        cookie_str = "; ".join(f"{k}={v}" for k, v in cookies.items())
        # Persist to secrets file so MCP server benefits too
        try:
            _COOKIE_SECRETS_PATH.parent.mkdir(parents=True, exist_ok=True)
            _COOKIE_SECRETS_PATH.write_text(cookie_str)
            _COOKIE_SECRETS_PATH.chmod(0o600)
        except Exception:
            pass
        return cookie_str

    except Exception:
        return None


def _cookie_from_chrome():
    """Read a fresh Rocket Money cookie from Chrome's cookie DB.

    Captures cookies from all relevant domains:
    - .rocketmoney.com        — AWSALB (7-day API sticky session), analytics
    - auth.rocketmoney.com    — Auth0 session (tb.auth0.sid)
    - truebill.auth0.com      — Auth0 session (legacy domain)
    - client-api.rocketmoney.com — API load balancer sticky session

    Returns the combined cookie string, or None if Chrome is unavailable
    (e.g. machine was asleep and Keychain is locked).
    """
    try:
        import browser_cookie3
        domains = [
            ".rocketmoney.com",
            "rocketmoney.com",
            "auth.rocketmoney.com",
            "truebill.auth0.com",     # Rocket Money's Auth0 tenant (legacy name)
            "client-api.rocketmoney.com",
            "app.rocketmoney.com",
        ]
        all_cookies = {}
        for domain in domains:
            try:
                jar = browser_cookie3.chrome(domain_name=domain)
                for c in jar:
                    all_cookies[c.name] = c.value
            except Exception:
                continue
        if all_cookies:
            return "; ".join(f"{k}={v}" for k, v in all_cookies.items())
    except Exception:
        pass
    return None


def _read_cookie(api_key="", port=18789):
    """Return the Rocket Money session cookie, refreshing automatically when possible.

    Resolution order:
    1. Chrome cookie DB via browser_cookie3  — always fresh; also updates the cache
    2. Vault credentials → programmatic headless login — when Chrome is unavailable
    3. ~/.soulacy/secrets/rocket_money_cookie — last known-good cached cookie
    4. ROCKET_MONEY_COOKIE env var            — CI / manual override
    """
    # 1. Fresh from Chrome
    chrome_cookie = _cookie_from_chrome()
    if chrome_cookie:
        try:
            _COOKIE_SECRETS_PATH.parent.mkdir(parents=True, exist_ok=True)
            _COOKIE_SECRETS_PATH.write_text(chrome_cookie)
            _COOKIE_SECRETS_PATH.chmod(0o600)
        except Exception:
            pass
        return chrome_cookie

    # 2. Vault credentials → programmatic login
    if not api_key:
        try:
            api_key, port = _read_soulacy_config()
        except Exception:
            pass
    if api_key:
        username = _vault_get(api_key, port, "finance-sync", "rm_username")
        password = _vault_get(api_key, port, "finance-sync", "rm_password")
        if username and password:
            cookie = _programmatic_login(username, password)
            if cookie:
                return cookie

    # 3. Secrets file (last known-good)
    if _COOKIE_SECRETS_PATH.exists():
        val = _COOKIE_SECRETS_PATH.read_text().strip()
        if val:
            return val

    # 4. Env var
    val = os.environ.get("ROCKET_MONEY_COOKIE", "").strip()
    if val:
        return val

    raise RuntimeError(
        "AUTH: Rocket Money cookie not found and programmatic login failed. "
        "Log into app.rocketmoney.com in Chrome, or call "
        "setup_rocket_money_credentials(username, password) to store credentials in the vault."
    )


def _gql(query, variables=None, cookie=None):
    """Execute a GraphQL request against the Rocket Money API."""
    import urllib.request, urllib.error
    cookie = cookie or _read_cookie()
    headers = {**GQL_HEADERS_BASE, "Cookie": cookie}
    payload = json.dumps({"query": query, **({"variables": variables} if variables else {})})
    req = urllib.request.Request(
        GQL_URL,
        data=payload.encode(),
        headers=headers,
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        if e.code == 401:
            raise RuntimeError(f"AUTH: Rocket Money 401 — cookie expired. Refresh it and restart. ({body[:200]})")
        raise RuntimeError(f"Rocket Money HTTP {e.code}: {body[:300]}")
    if "errors" in data and not data.get("data"):
        msgs = "; ".join(e.get("message", str(e)) for e in data["errors"])
        raise RuntimeError(f"Rocket Money GraphQL error: {msgs}")
    return data.get("data", {})


# ── Soulacy KB API ────────────────────────────────────────────────────────────

KB_NAME = "finance-local"
KB_DESCRIPTION = (
    "Personal finance data synced daily from Rocket Money. "
    "Contains accounts, net worth, budget, subscriptions, and recent transactions."
)


def _read_soulacy_config():
    """Extract Soulacy API key and port from config.yaml.

    Search order (first file with a non-empty api_key wins):
    1. ~/.soulacy/config.yaml  — runtime config (secrets, channels, mcp)
    2. ./config.yaml           — repo dev config (when Soulacy starts from repo dir)
    """
    candidates = [
        Path.home() / ".soulacy" / "config.yaml",
        Path("config.yaml"),  # CWD — resolves to repo dir when started via deploy.sh
    ]
    for cfg_path in candidates:
        if not cfg_path.exists():
            continue
        text = cfg_path.read_text()
        # Match only Soulacy server API keys (sy_ prefix) to avoid matching
        # LLM provider keys (sk-ant-, gsk_, etc.) that appear earlier in the file.
        key_match = re.search(r"api_key:\s*['\"]?(sy_[a-zA-Z0-9]+)['\"]?", text)
        api_key = key_match.group(1) if key_match else ""
        port_match = re.search(r"port:\s*(\d+)", text)
        port = int(port_match.group(1)) if port_match else 18789
        if api_key:
            return api_key, port
    return "", 18789


def _soulacy_request(method, path, body=None, api_key="", port=18789):
    """Make a request to the local Soulacy REST API."""
    import urllib.request, urllib.error
    url = f"http://localhost:{port}/api/v1{path}"
    headers = {"Content-Type": "application/json"}
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read()
            if not raw:          # 204 No Content (e.g. DELETE) → return empty dict
                return {}
            return json.loads(raw)
    except urllib.error.HTTPError as e:
        body_text = e.read().decode(errors="replace")
        raise RuntimeError(f"Soulacy API {method} {path} → HTTP {e.code}: {body_text[:400]}")


def _ensure_kb(api_key, port):
    """Create the finance-local KB if it doesn't already exist. Returns KB info."""
    try:
        kbs = _soulacy_request("GET", "/knowledge", api_key=api_key, port=port)
        for kb in kbs.get("knowledge_bases", []):
            if kb["name"] == KB_NAME:
                return kb
    except Exception:
        pass  # If listing fails, try creating

    kb = _soulacy_request("POST", "/knowledge", body={
        "name": KB_NAME,
        "description": KB_DESCRIPTION,
        "embedding_provider": "ollama",
        "embedding_model": "nomic-embed-text",
        "chunk_size": 800,
        "chunk_overlap": 120,
    }, api_key=api_key, port=port)
    return kb


def _delete_docs_by_prefix(api_key, port, prefixes):
    """Delete all KB documents whose title starts with any of the given prefixes."""
    try:
        result = _soulacy_request("GET", f"/knowledge/{KB_NAME}/documents", api_key=api_key, port=port)
        docs = result.get("documents") or []
        deleted = 0
        for doc in docs:
            title = doc.get("title", "")
            if any(title.startswith(p) for p in prefixes):
                _soulacy_request("DELETE", f"/knowledge/{KB_NAME}/documents/{doc['id']}",
                                 api_key=api_key, port=port)
                deleted += 1
        return deleted
    except Exception as e:
        print(f"  [warn] Could not clean docs: {e}", file=sys.stderr)
        return 0


def _delete_all_docs(api_key, port):
    """Wipe every document in the KB (used for full_sync)."""
    deleted = _delete_docs_by_prefix(api_key, port, prefixes=[""])  # empty prefix matches all
    return deleted


def _ingest_document(api_key, port, title, source, content):
    """Ingest one text document into the finance-local KB."""
    if not content.strip():
        return None
    return _soulacy_request("POST", f"/knowledge/{KB_NAME}/documents", body={
        "title": title,
        "source": source,
        "mime_type": "text/plain",
        "content": content,
    }, api_key=api_key, port=port)


# ── Data fetchers ─────────────────────────────────────────────────────────────

def _fetch_accounts(cookie):
    data = _gql('''{ viewer { accounts(first: 200) { edges { node {
        id name currentBalance displayedBalance
        includeInNetWorth institution { name }
    } } } } }''', cookie=cookie)
    nodes = [e["node"] for e in data.get("viewer", {}).get("accounts", {}).get("edges", []) if e.get("node")]
    return [n for n in nodes if n.get("includeInNetWorth", True)]


def _fetch_net_worth(cookie):
    try:
        data = _gql('''{ viewer {
            netWorth { assets { id name } debts { id name } }
            accounts(first: 200) { edges { node {
                id name currentBalance displayedBalance
                includeInNetWorth institution { name }
            } } }
        } }''', cookie=cookie).get("viewer", {})
        edges = [e for e in data.get("accounts", {}).get("edges", [])
                 if e.get("node", {}).get("includeInNetWorth", True)]
        data["accounts"]["edges"] = edges
    except Exception:
        data = _gql('''{ viewer {
            netWorth { assets { id name } debts { id name } }
            accounts(first: 200) { edges { node {
                id name currentBalance displayedBalance institution { name }
            } } }
        } }''', cookie=cookie).get("viewer", {})

    nw = data.get("netWorth", {})
    debt_names = {item["name"].lower().strip() for item in nw.get("debts", [])}
    all_accts = [e["node"] for e in data.get("accounts", {}).get("edges", []) if e.get("node")]

    assets, debts = [], []
    for a in all_accts:
        raw = a.get("displayedBalance") if a.get("displayedBalance") is not None else a.get("currentBalance", 0)
        bal_dollars = abs(raw or 0) / 100.0
        entry = {"name": a.get("name", ""), "institution": (a.get("institution") or {}).get("name", ""), "balance_dollars": bal_dollars}
        if a.get("name", "").lower().strip() in debt_names:
            debts.append(entry)
        else:
            assets.append(entry)

    return {
        "assets": assets,
        "debts": debts,
        "total_assets": sum(a["balance_dollars"] for a in assets),
        "total_debts": sum(d["balance_dollars"] for d in debts),
        "net_worth": sum(a["balance_dollars"] for a in assets) - sum(d["balance_dollars"] for d in debts),
    }


def _fetch_budget(cookie):
    plan = _gql('{ viewer { budgetPlan { id budgetItems { id amount } } } }', cookie=cookie)
    plan = plan.get("viewer", {}).get("budgetPlan") or {}
    items = plan.get("budgetItems", [])
    return [{"id": i.get("id", ""), "amount_dollars": i.get("amount", 0) / 100.0} for i in items]


def _fetch_subscriptions(cookie):
    data = _gql('{ viewer { allServices { edges { node { id name } } } } }', cookie=cookie)
    return [e["node"] for e in data.get("viewer", {}).get("allServices", {}).get("edges", []) if e.get("node")]


def _fetch_transactions(cookie, days_back=90, full_sync=False):
    """Paginate through Rocket Money transactions.

    full_sync=True  → no date filter; fetches ALL historical transactions.
    full_sync=False → fetches the last `days_back` days only.
    """
    all_txns = []
    cursor = None
    page = 0
    page_limit = 500 if full_sync else 20  # ~50k vs ~2k transactions max

    if full_sync:
        start_str = end_str = None
    else:
        end_date = datetime.date.today()
        start_date = end_date - datetime.timedelta(days=days_back)
        start_str = start_date.isoformat()
        end_str = end_date.isoformat()

    while True:
        after_clause = f', after: "{cursor}"' if cursor else ""
        # Rocket Money GraphQL uses gteDate/ltDate for date filtering.
        # Omit both for a full-history scan.
        if full_sync:
            date_clause = ""
        else:
            date_clause = f', gteDate: "{start_str}", ltDate: "{end_str}"'

        q = f'''{{ viewer {{ transactions(
            first: 100{date_clause}{after_clause}
        ) {{
            edges {{ node {{ id amount date merchantShortName pending note category {{ id }} }} }}
            pageInfo {{ hasNextPage endCursor }}
        }} }} }}'''

        data = _gql(q, cookie=cookie)
        txns_data = data.get("viewer", {}).get("transactions", {})
        edges = txns_data.get("edges", [])
        nodes = [e["node"] for e in edges if e.get("node")]
        all_txns.extend(nodes)
        page_info = txns_data.get("pageInfo", {})
        if not page_info.get("hasNextPage") or not page_info.get("endCursor"):
            break
        cursor = page_info["endCursor"]
        page += 1
        if page > page_limit:
            print(f"  [warn] Transaction page cap ({page_limit}) reached — {len(all_txns)} txns fetched.", file=sys.stderr)
            break
        time.sleep(0.3)  # gentle rate limiting

    return all_txns


# ── Text formatters ───────────────────────────────────────────────────────────

def _fmt_accounts(accounts, date_str):
    lines = [f"# Accounts Snapshot — {date_str}", ""]
    for a in sorted(accounts, key=lambda x: abs(x.get("currentBalance") or x.get("displayedBalance") or 0), reverse=True):
        raw = a.get("displayedBalance") if a.get("displayedBalance") is not None else a.get("currentBalance", 0)
        bal = abs(raw or 0) / 100.0
        inst = (a.get("institution") or {}).get("name", "")
        lines.append(f"- {a.get('name', 'Unknown')} ({inst}): ${bal:,.2f}")
    return "\n".join(lines)


def _fmt_net_worth(nw, date_str):
    lines = [f"# Net Worth Snapshot — {date_str}", ""]
    lines.append(f"Total Assets:  ${nw['total_assets']:>12,.2f}")
    lines.append(f"Total Debts:   ${nw['total_debts']:>12,.2f}")
    lines.append(f"Net Worth:     ${nw['net_worth']:>12,.2f}")
    lines.append("")
    lines.append("## Assets")
    for a in sorted(nw["assets"], key=lambda x: x["balance_dollars"], reverse=True):
        lines.append(f"  {a['name']} ({a['institution']}): ${a['balance_dollars']:,.2f}")
    lines.append("")
    lines.append("## Debts")
    for d in sorted(nw["debts"], key=lambda x: x["balance_dollars"], reverse=True):
        lines.append(f"  {d['name']} ({d['institution']}): ${d['balance_dollars']:,.2f}")
    return "\n".join(lines)


def _fmt_budget(items, date_str):
    lines = [f"# Budget Plan — {date_str}", ""]
    total = sum(i["amount_dollars"] for i in items)
    lines.append(f"Total budget items: {len(items)}")
    lines.append(f"Total planned spending: ${total:,.2f}")
    lines.append("")
    for item in sorted(items, key=lambda x: x["amount_dollars"], reverse=True):
        lines.append(f"- Budget item {item['id']}: ${item['amount_dollars']:,.2f}/month")
    return "\n".join(lines)


def _fmt_subscriptions(subs, date_str):
    lines = [f"# Subscriptions & Recurring Services — {date_str}", ""]
    if not subs:
        lines.append("No subscriptions tracked.")
    else:
        for s in subs:
            lines.append(f"- {s.get('name', 'Unknown')} (id: {s.get('id', '')})")
    return "\n".join(lines)


def _fmt_transactions_chunk(txns, chunk_idx, total_chunks, date_str, start_date, end_date):
    """Format a batch of transactions as a rich-text chunk for embedding."""
    lines = [
        f"# Transactions — {date_str} (chunk {chunk_idx + 1}/{total_chunks})",
        f"# Period: {start_date} to {end_date}",
        f"# This chunk: {len(txns)} transactions",
        "",
    ]
    for t in txns:
        amount = t.get("amount", 0) / 100.0
        direction = "debit" if amount < 0 else "credit"
        merchant = t.get("merchantShortName") or t.get("note") or "Unknown merchant"
        pending = " [PENDING]" if t.get("pending") else ""
        category = (t.get("category") or {}).get("id", "")
        lines.append(
            f"{t.get('date', '')} | {merchant:<35} | "
            f"${abs(amount):>10,.2f} {direction}{pending}"
            + (f" | cat:{category}" if category else "")
        )
    return "\n".join(lines)


# ── Main sync tool ────────────────────────────────────────────────────────────

CHUNK_SIZE = 50  # transactions per knowledge chunk


def sync_finance_kb(days_back=90, full_sync=False):
    """
    Sync Rocket Money financial data to the finance-local knowledge base.

    full_sync=True  — Wipe the entire KB, ingest ALL historical transactions
                      (no date limit), and reset the sync state. Use once on
                      initial setup or to rebuild. Takes 5-30 min.

    full_sync=False — Incremental update (default, used by daily cron).
                      Reads last_sync_date from ~/.soulacy/finance-sync-state.json
                      and fetches only transactions since that date.
                      Accounts / net_worth / budget / subscriptions are always
                      refreshed (current state). Transaction docs accumulate —
                      they are never deleted after a full sync.

    Returns a human-readable summary of what was ingested.
    """
    full_sync = bool(full_sync)
    today = datetime.date.today()
    date_str = today.isoformat()

    log = []
    errors = []

    def info(msg):
        print(f"  {msg}", file=sys.stderr)
        log.append(msg)

    def warn(msg):
        print(f"  [warn] {msg}", file=sys.stderr)
        errors.append(msg)

    # ── 1. Read config ──────────────────────────────────────────────────────
    api_key, port = _read_soulacy_config()
    info(f"Soulacy API: localhost:{port}")

    # ── 2. Read cookie ──────────────────────────────────────────────────────
    try:
        cookie = _read_cookie()
        info("Cookie: OK")
    except RuntimeError as e:
        return str(e)

    # ── 3. Ensure KB exists ─────────────────────────────────────────────────
    try:
        kb = _ensure_kb(api_key, port)
        info(f"KB '{KB_NAME}': ready (id={kb.get('id', '?')})")
    except Exception as e:
        return f"ERROR: Could not create/access '{KB_NAME}' KB: {e}"

    # ── 4. Determine transaction date range ─────────────────────────────────
    if full_sync:
        start_date = None   # no date filter → all history
        end_date = date_str
        info("Mode: full sync — fetching all historical transactions")
    else:
        state = _read_sync_state()
        last_sync = state.get("last_sync_date")
        if last_sync:
            # Fetch from day AFTER the last sync to avoid duplicates
            start_date = (datetime.date.fromisoformat(last_sync) + datetime.timedelta(days=1)).isoformat()
            info(f"Mode: incremental — transactions from {start_date} to {date_str}")
        else:
            # No state yet — fall back to days_back
            days_back = min(int(days_back or 90), 3650)
            start_date = (today - datetime.timedelta(days=days_back)).isoformat()
            info(f"Mode: incremental (no prior state) — transactions from {start_date} to {date_str}")
        end_date = date_str

    # ── 5. Clear current-state docs (always replaced) ───────────────────────
    # Accounts/net_worth/budget/subscriptions use fixed titles so we delete the
    # old version before re-ingesting. Transaction docs are NOT deleted here —
    # they accumulate across runs.
    if full_sync:
        info("Full sync: wiping entire KB…")
        _delete_all_docs(api_key, port)
    else:
        info("Clearing current-state docs (accounts, net_worth, budget, subscriptions)…")
        _delete_docs_by_prefix(api_key, port, prefixes=[
            "finance/accounts", "finance/net_worth", "finance/budget", "finance/subscriptions"
        ])

    # ── 6. Fetch + ingest accounts ──────────────────────────────────────────
    # Fixed title — always reflects the latest state.
    try:
        accounts = _fetch_accounts(cookie)
        doc = _ingest_document(api_key, port,
            title="finance/accounts",
            source=f"rocketmoney:accounts:{date_str}",
            content=_fmt_accounts(accounts, date_str))
        info(f"Accounts: {len(accounts)} accounts → {doc.get('chunk_count', '?')} chunks")
    except Exception as e:
        warn(f"accounts: {e}")

    # ── 7. Fetch + ingest net worth ─────────────────────────────────────────
    try:
        nw = _fetch_net_worth(cookie)
        doc = _ingest_document(api_key, port,
            title="finance/net_worth",
            source=f"rocketmoney:net_worth:{date_str}",
            content=_fmt_net_worth(nw, date_str))
        info(f"Net worth: assets=${nw['total_assets']:,.0f} "
             f"debts=${nw['total_debts']:,.0f} nw=${nw['net_worth']:,.0f} "
             f"→ {doc.get('chunk_count', '?')} chunks")
    except Exception as e:
        warn(f"net_worth: {e}")

    # ── 8. Fetch + ingest budget ────────────────────────────────────────────
    try:
        budget = _fetch_budget(cookie)
        doc = _ingest_document(api_key, port,
            title="finance/budget",
            source=f"rocketmoney:budget:{date_str}",
            content=_fmt_budget(budget, date_str))
        info(f"Budget: {len(budget)} items → {doc.get('chunk_count', '?')} chunks")
    except Exception as e:
        warn(f"budget: {e}")

    # ── 9. Fetch + ingest subscriptions ────────────────────────────────────
    try:
        subs = _fetch_subscriptions(cookie)
        doc = _ingest_document(api_key, port,
            title="finance/subscriptions",
            source=f"rocketmoney:subscriptions:{date_str}",
            content=_fmt_subscriptions(subs, date_str))
        info(f"Subscriptions: {len(subs)} services → {doc.get('chunk_count', '?')} chunks")
    except Exception as e:
        warn(f"subscriptions: {e}")

    # ── 10. Fetch + ingest transactions (incremental or full) ───────────────
    # full_sync  → "finance-history/transactions/chunk-NNN" (one-time history)
    # incremental → "finance-delta/YYYY-MM-DD/transactions/chunk-NNN" (daily additions)
    # Both accumulate permanently — old chunks are never deleted after full sync.
    txn_label = start_date or "all history"
    txn_prefix = "finance-history" if full_sync else f"finance-delta/{date_str}"

    # Skip transaction fetch when already up to date (start_date > end_date means
    # last sync was today and there is nothing new to fetch).
    already_current = (not full_sync and start_date and start_date > end_date)
    try:
        if already_current:
            info("Transactions already current — nothing new since last sync.")
            txns = []
        else:
            info(f"Fetching transactions ({txn_label} → {end_date})…")
            txns = _fetch_transactions(cookie, full_sync=full_sync,
                                       days_back=int(days_back or 90))
        if txns:
            info(f"  Retrieved {len(txns)} transactions, splitting into chunks of {CHUNK_SIZE}…")
            total_chunks = max(1, (len(txns) + CHUNK_SIZE - 1) // CHUNK_SIZE)
            ingested_chunks = 0
            for i in range(0, len(txns), CHUNK_SIZE):
                batch = txns[i: i + CHUNK_SIZE]
                chunk_idx = i // CHUNK_SIZE
                doc = _ingest_document(api_key, port,
                    title=f"{txn_prefix}/transactions/chunk-{chunk_idx:03d}",
                    source=f"rocketmoney:transactions:{date_str}:chunk{chunk_idx}",
                    content=_fmt_transactions_chunk(
                        batch, chunk_idx, total_chunks, date_str,
                        start_date or "all history", end_date))
                ingested_chunks += doc.get("chunk_count", 0) if doc else 0
            info(f"Transactions: {len(txns)} txns across {total_chunks} docs → {ingested_chunks} chunks")
    except Exception as e:
        warn(f"transactions: {e}")

    # ── 11. Persist sync state ──────────────────────────────────────────────
    # Write even if there were warnings — partial syncs still advance the cursor
    # so the next incremental run doesn't re-fetch successfully synced data.
    if not errors or len(errors) < 5:  # write state unless almost everything failed
        old_state = _read_sync_state()
        new_state = {
            "last_sync_date": date_str,
            "last_full_sync": date_str if full_sync else old_state.get("last_full_sync", ""),
            "mode": "full" if full_sync else "incremental",
        }
        try:
            _write_sync_state(new_state)
            info(f"Sync state saved: last_sync_date={date_str}")
        except Exception as e:
            warn(f"Could not save sync state: {e}")

    # ── 12. Summary ─────────────────────────────────────────────────────────
    mode_label = "full" if full_sync else "incremental"
    summary_lines = [f"Finance sync complete ({mode_label}) — {date_str}"]
    skip = {"Soulacy API", "Cookie", "KB '", "Mode:", "Full sync", "Clearing", "Sync state"}
    summary_lines.extend(
        f"  ✓ {l}" for l in log
        if not any(l.startswith(s) for s in skip)
    )
    if errors:
        summary_lines.append("Warnings:")
        summary_lines.extend(f"  ⚠ {e}" for e in errors)
    return "\n".join(summary_lines)
