# Built-in Tools

Agents can be equipped with tools that extend their capabilities beyond text generation. Tools are declared in SOUL.yaml and invoked autonomously by the LLM.

## Available tools

| Tool | Description |
|------|-------------|
| `web_search` | Search the web via DuckDuckGo |
| `url_fetch` | Fetch and extract text from a URL |
| `calculator` | Evaluate mathematical expressions |
| `code_interpreter` | Execute Python code in a sandbox |
| `image_analyze` | Analyze an image passed in the conversation |

---

## web_search

Searches the web and returns a summary of the top results.

```yaml
tools:
  - web_search
```

**Parameters** (set by the LLM automatically):

| Parameter | Type | Description |
|-----------|------|-------------|
| `query` | string | The search query |
| `num_results` | int | Number of results (default: 5) |

**Example LLM invocation:**

```json
{
  "tool": "web_search",
  "query": "latest news on AI agents 2025",
  "num_results": 3
}
```

---

## url_fetch

Fetches a URL and returns the cleaned text content. Useful for reading articles, documentation, or API responses.

```yaml
tools:
  - url_fetch
```

| Parameter | Type | Description |
|-----------|------|-------------|
| `url` | string | URL to fetch |
| `max_chars` | int | Truncate response to this many characters (default: 8000) |

---

## calculator

Evaluates a mathematical expression safely.

```yaml
tools:
  - calculator
```

| Parameter | Type | Description |
|-----------|------|-------------|
| `expression` | string | Math expression to evaluate (e.g. `"2^10 + sqrt(144)"`) |

Supports: arithmetic, powers, square roots, trigonometry, logarithms.

---

## code_interpreter

Executes Python code in an isolated sandbox and returns stdout. Useful for data processing, calculations, and file manipulation.

```yaml
tools:
  - code_interpreter
```

| Parameter | Type | Description |
|-----------|------|-------------|
| `code` | string | Python code to execute |
| `timeout` | int | Execution timeout in seconds (default: 10) |

!!! warning "Sandbox"
    Code runs in a restricted subprocess with no network access and limited filesystem. Output is capped at 4096 characters.

---

## image_analyze

Analyzes an image passed as a base64 data URI or URL (requires a vision-capable model such as `gpt-4o`).

```yaml
model: gpt-4o
tools:
  - image_analyze
```

| Parameter | Type | Description |
|-----------|------|-------------|
| `image_url` | string | URL or `data:image/...;base64,...` |
| `prompt` | string | What to analyze or extract |

---

## Combining tools

Tools compose naturally. An agent with both `web_search` and `url_fetch` can search, pick the best result, fetch its full text, and summarize — all in one turn:

```yaml
name: deep-researcher
model: gpt-4o
system_prompt: |
  You are a thorough researcher. Search for information, fetch relevant pages,
  and provide well-cited summaries.
tools:
  - web_search
  - url_fetch
  - calculator
channels:
  - http
```
