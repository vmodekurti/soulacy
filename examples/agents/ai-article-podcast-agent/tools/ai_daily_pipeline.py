"""
ai_daily_pipeline — Soulacy Python tool
========================================
Aggregates AI news from ~25 sources, ranks articles, creates a date-tagged
NotebookLM notebook, generates an Audio Overview, and delivers the link via
Telegram. Called by the ai-article-podcast-agent cron at 7 AM daily.

Function signature matches the SOUL.yaml tool definition.
"""
import os, sys, json, subprocess, datetime, time, re
from pathlib import Path


# ── Sources ────────────────────────────────────────────────────────────────────
# (name, rss_url, weight 1-5, tags for scoring)
SOURCES = [
    # AI Labs
    ("OpenAI Blog",       "https://openai.com/blog/rss.xml",                               5, ["lab"]),
    ("Anthropic",         "https://www.anthropic.com/rss.xml",                              5, ["lab"]),
    ("Google DeepMind",   "https://deepmind.google/blog/rss.xml",                           5, ["lab"]),
    ("Google AI Blog",    "https://blog.google/technology/ai/rss/",                         4, ["lab"]),
    ("Meta AI",           "https://ai.meta.com/blog/feed/",                                 4, ["lab"]),
    ("Hugging Face",      "https://huggingface.co/blog/feed.xml",                           4, ["lab", "research"]),
    ("Mistral AI",        "https://mistral.ai/news/rss/",                                   4, ["lab"]),
    # Founder / operator blogs
    ("Sam Altman",        "https://blog.samaltman.com/posts.atom",                          5, ["founder"]),
    ("Andrej Karpathy",   "https://karpathy.github.io/feed.xml",                            5, ["founder", "research"]),
    ("LangChain Blog",    "https://blog.langchain.dev/rss/",                                4, ["framework", "agents"]),
    # Expert commentary
    ("Import AI",         "https://jack-clark.net/feed/",                                   5, ["expert"]),
    ("One Useful Thing",  "https://www.oneusefulthing.org/feed",                            5, ["expert"]),
    ("AI Snake Oil",      "https://aisnakeoil.substack.com/feed",                          4, ["expert"]),
    ("Last Week in AI",   "https://lastweekin.ai/feed",                                     4, ["expert", "roundup"]),
    ("Stratechery",       "https://stratechery.com/feed/",                                  4, ["expert"]),
    # Research
    ("arXiv cs.AI",       "https://rss.arxiv.org/rss/cs.AI",                               3, ["research"]),
    ("arXiv cs.LG",       "https://rss.arxiv.org/rss/cs.LG",                               3, ["research"]),
    # Tech press
    ("TechCrunch AI",     "https://techcrunch.com/category/artificial-intelligence/feed/", 3, ["press"]),
    ("The Verge AI",      "https://www.theverge.com/rss/ai-artificial-intelligence/index.xml", 3, ["press"]),
    ("VentureBeat AI",    "https://venturebeat.com/category/ai/feed/",                      3, ["press"]),
    ("Ars Technica",      "https://feeds.arstechnica.com/arstechnica/technology-lab",       3, ["press"]),
    ("Wired AI",          "https://www.wired.com/feed/category/artificial-intelligence/latest/rss", 3, ["press"]),
    ("MIT Tech Review",   "https://www.technologyreview.com/topic/artificial-intelligence/feed", 4, ["press"]),
]

# Priority keywords — articles matching these score higher
PRIORITY_KEYWORDS = [
    # Agentic AI
    "agent", "agentic", "mcp", "computer use", "tool use", "autonomous",
    "langgraph", "crewai", "autogen", "multi-agent",
    # Capabilities
    "reasoning", "frontier", "multimodal", "context window", "throughput",
    "benchmark", "capability", "intelligence",
    # Workflow / enterprise
    "workflow", "automation", "enterprise", "productivity", "knowledge work",
    # Founder voices
    "altman", "amodei", "hassabis", "karpathy", "mollick", "lecun",
    # Major releases
    "gpt-5", "claude 4", "gemini", "llama 4", "grok", "mistral",
    "release", "launch", "announced",
]


# ── Article fetching & scoring ─────────────────────────────────────────────────

def _fetch_feed(name, url, weight, tags, max_age_hours, cutoff_dt, results):
    """Fetch one RSS feed and append scored articles to results."""
    try:
        import feedparser
        feed = feedparser.parse(url)
        for entry in feed.entries[:20]:  # cap per-source to avoid overloading
            # Parse published date
            pub = None
            if hasattr(entry, "published_parsed") and entry.published_parsed:
                pub = datetime.datetime(*entry.published_parsed[:6],
                                        tzinfo=datetime.timezone.utc)
            elif hasattr(entry, "updated_parsed") and entry.updated_parsed:
                pub = datetime.datetime(*entry.updated_parsed[:6],
                                        tzinfo=datetime.timezone.utc)

            if pub and pub < cutoff_dt:
                continue  # too old

            link = getattr(entry, "link", None)
            if not link or not link.startswith("http"):
                continue

            title   = getattr(entry, "title", "")
            summary = getattr(entry, "summary", "")
            text    = (title + " " + summary).lower()

            # Score: base = source weight (1-5)
            score = float(weight)
            # +0.3 per priority keyword match (capped at +3)
            kw_hits = sum(1 for kw in PRIORITY_KEYWORDS if kw in text)
            score += min(kw_hits * 0.3, 3.0)
            # Recency bonus: 0-1 based on age
            if pub:
                age_h = (datetime.datetime.now(datetime.timezone.utc) - pub).total_seconds() / 3600
                recency = max(0.0, 1.0 - (age_h / max_age_hours))
                score += recency
            else:
                score += 0.5  # unknown age, assume recent-ish

            results.append({
                "title":   title,
                "url":     link,
                "source":  name,
                "score":   score,
                "pub":     pub.isoformat() if pub else None,
            })
    except Exception as e:
        print(f"  [warn] {name}: {e}", file=sys.stderr)


def _fetch_articles(max_age_hours):
    """Fetch and rank articles from all sources. Returns list sorted by score."""
    import concurrent.futures
    cutoff = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=max_age_hours)
    results = []
    lock = __import__("threading").Lock()

    def fetch_one(args):
        name, url, weight, tags = args
        local = []
        _fetch_feed(name, url, weight, tags, max_age_hours, cutoff, local)
        with lock:
            results.extend(local)

    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        pool.map(fetch_one, SOURCES)

    # Dedupe by URL
    seen = set()
    unique = []
    for art in results:
        if art["url"] not in seen:
            seen.add(art["url"])
            unique.append(art)

    unique.sort(key=lambda x: x["score"], reverse=True)
    return unique


# ── NotebookLM helpers ─────────────────────────────────────────────────────────

def _nlm(cmd, timeout=120):
    """Run a notebooklm CLI command and return (returncode, stdout, stderr)."""
    result = subprocess.run(
        ["notebooklm"] + cmd,
        capture_output=True, text=True, timeout=timeout
    )
    return result.returncode, result.stdout.strip(), result.stderr.strip()


def _nlm_json(cmd, timeout=120):
    """Run notebooklm command with --json, parse and return the result dict."""
    rc, out, err = _nlm(cmd + ["--json"], timeout=timeout)
    if rc != 0:
        raise RuntimeError(f"notebooklm {' '.join(cmd)} failed (rc={rc}): {err}")
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        raise RuntimeError(f"notebooklm returned invalid JSON: {out!r}")


# ── Telegram helper ────────────────────────────────────────────────────────────

def _read_telegram_token():
    """Read the Telegram bot token from ~/.soulacy/config.yaml."""
    try:
        import yaml
        cfg = yaml.safe_load(Path.home().joinpath(".soulacy", "config.yaml").read_text())
        tg = cfg.get("channels", {}).get("telegram", {})
        # Multi-bot config
        bots = tg.get("bots", [])
        if bots:
            return bots[0]["token"]
        # Single-bot config
        return tg.get("token", "")
    except Exception as e:
        print(f"  [warn] Could not read Telegram token: {e}", file=sys.stderr)
        return os.environ.get("TELEGRAM_BOT_TOKEN", "")


def _send_telegram(chat_id, text, token):
    """Send a Telegram message using the Bot API."""
    import httpx
    try:
        r = httpx.post(
            f"https://api.telegram.org/bot{token}/sendMessage",
            json={"chat_id": chat_id, "text": text, "parse_mode": "HTML"},
            timeout=15,
        )
        r.raise_for_status()
    except Exception as e:
        print(f"  [warn] Telegram send failed: {e}", file=sys.stderr)


# ── Main pipeline ──────────────────────────────────────────────────────────────

def ai_daily_pipeline(chat_id, limit=10, max_age_hours=24):
    """
    Full daily AI podcast pipeline.

    1. Aggregate and rank AI articles from ~25 sources.
    2. Create a date-tagged NotebookLM notebook.
    3. Add top `limit` articles as web sources.
    4. Generate an Audio Overview.
    5. Wait up to 30 minutes for the audio to render.
    6. Send the notebook link via Telegram.
    7. Return a status string.
    """
    today = datetime.date.today().strftime("%Y-%m-%d")
    tg_token = _read_telegram_token()

    print(f"[1/5] Fetching articles (last {max_age_hours}h)...", file=sys.stderr)
    articles = _fetch_articles(max_age_hours)
    if not articles:
        msg = f"⚠️ AI Daily {today}: no articles fetched from any source."
        _send_telegram(chat_id, msg, tg_token)
        return msg

    top = articles[:limit]
    print(f"      {len(articles)} total, using top {len(top)}.", file=sys.stderr)
    for i, a in enumerate(top, 1):
        print(f"      {i:2}. [{a['score']:.1f}] {a['source']}: {a['title'][:70]}", file=sys.stderr)

    # 2. Create notebook
    print(f"[2/5] Creating NotebookLM notebook...", file=sys.stderr)
    nb_data = _nlm_json(["create", f"AI Daily — {today}"])
    notebook_id = nb_data["notebook"]["id"]
    notebook_url = f"https://notebooklm.google.com/notebook/{notebook_id}"
    print(f"      Notebook: {notebook_id}", file=sys.stderr)

    # 3. Add sources
    print(f"[3/5] Adding {len(top)} sources...", file=sys.stderr)
    source_ids = []
    for art in top:
        try:
            src_data = _nlm_json(["source", "add", art["url"], "--notebook", notebook_id])
            source_ids.append(src_data["source"]["id"])
            print(f"      + {art['source']}: {art['title'][:60]}", file=sys.stderr)
        except Exception as e:
            print(f"      [warn] skip {art['url'][:60]}: {e}", file=sys.stderr)

    if not source_ids:
        msg = f"⚠️ AI Daily {today}: notebook created but all sources failed to add. {notebook_url}"
        _send_telegram(chat_id, msg, tg_token)
        return msg

    # Wait for sources to process (parallel, up to 90s each)
    print(f"[4/5] Waiting for {len(source_ids)} sources to process...", file=sys.stderr)
    for sid in source_ids:
        subprocess.run(
            ["notebooklm", "source", "wait", sid, "-n", notebook_id, "--timeout", "90"],
            capture_output=True, timeout=100,
        )

    # 4. Generate audio
    print(f"[5/5] Generating Audio Overview...", file=sys.stderr)
    try:
        gen_data = _nlm_json([
            "generate", "audio",
            (
                "Focus on agentic AI, new capabilities, and workflow automation. "
                "Lead with the most impactful stories. Keep it sharp and actionable."
            ),
            "--notebook", notebook_id,
        ], timeout=60)
        artifact_id = gen_data.get("task_id", "")
    except Exception as e:
        # Audio generation rate-limited — notebook still useful
        print(f"      [warn] Audio generation failed: {e}", file=sys.stderr)
        artifact_id = ""

    # Wait for audio (up to 30 min) — send link regardless of outcome
    audio_ready = False
    if artifact_id:
        print(f"      Waiting for audio (artifact {artifact_id[:8]}..., timeout 30m)...", file=sys.stderr)
        rc, _, _ = _nlm(
            ["artifact", "wait", artifact_id, "-n", notebook_id, "--timeout", "1800"],
            timeout=1850,
        )
        audio_ready = (rc == 0)

    # 5. Deliver via Telegram
    source_list = "\n".join(
        f"  • {a['source']}: {a['title'][:55]}" for a in top[:5]
    )
    if audio_ready:
        msg = (
            f"🎙 <b>AI Daily Podcast — {today}</b>\n\n"
            f"Top stories from {len(source_ids)} sources:\n{source_list}\n"
            f"{'  + ' + str(len(top)-5) + ' more' if len(top) > 5 else ''}\n\n"
            f"<a href='{notebook_url}'>▶ Open in NotebookLM</a>"
        )
        status = f"✓ AI Daily {today}: {len(source_ids)} sources, audio ready. {notebook_url}"
    elif artifact_id:
        msg = (
            f"🎙 <b>AI Daily — {today}</b>\n\n"
            f"{len(source_ids)} sources loaded. Audio still rendering — check in a few minutes.\n\n"
            f"<a href='{notebook_url}'>▶ Open in NotebookLM</a>"
        )
        status = f"✓ AI Daily {today}: {len(source_ids)} sources, audio rendering. {notebook_url}"
    else:
        msg = (
            f"🎙 <b>AI Daily — {today}</b>\n\n"
            f"{len(source_ids)} sources loaded (audio generation unavailable today — rate limit).\n\n"
            f"<a href='{notebook_url}'>▶ Open in NotebookLM</a>"
        )
        status = f"✓ AI Daily {today}: {len(source_ids)} sources, no audio (rate limit). {notebook_url}"

    _send_telegram(chat_id, msg, tg_token)
    print(f"\n{status}", file=sys.stderr)
    return status
