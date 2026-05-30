#!/usr/bin/env python3
"""
Soulacy Financial Ingestion Tool
Reads config.yaml (in CWD or ~/.soulacy) to discover host, port, and api_key,
then ingests the data into the local SQLite vector database.
"""
import sys
import json
import os
import re
import urllib.request
import urllib.parse

def load_config():
    """Load host, port, and api_key from config.yaml using zero-dependency regex."""
    paths = [
        "config.yaml",
        os.path.expanduser("~/.soulacy/config.yaml")
    ]
    config = {"host": "127.0.0.1", "port": 18789, "api_key": ""}
    for p in paths:
        if os.path.exists(p):
            try:
                with open(p, "r", encoding="utf-8") as f:
                    content = f.read()
                
                # Parse host
                host_match = re.search(r"host:\s*[\"']?([a-zA-Z0-9\.-]+)[\"']?", content)
                if host_match:
                    config["host"] = host_match.group(1).strip()
                
                # Parse port
                port_match = re.search(r"port:\s*(\d+)", content)
                if port_match:
                    config["port"] = int(port_match.group(1).strip())
                
                # Parse api_key
                key_match = re.search(r"api_key:\s*[\"']?([a-zA-Z0-9_-]+)[\"']?", content)
                if key_match:
                    config["api_key"] = key_match.group(1).strip()
                
                break
            except Exception as e:
                print(f"[Warning] Failed to read config from {p}: {e}", file=sys.stderr)
    return config

def ingest_document(title, content, kb_name="financials"):
    cfg = load_config()
    host = cfg["host"]
    port = cfg["port"]
    api_key = cfg["api_key"]
    
    headers = {
        "Content-Type": "application/json",
    }
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
        
    # 1. Ensure the knowledge base exists (create if not)
    kb_url = f"http://{host}:{port}/api/v1/knowledge"
    kb_payload = {
        "name": kb_name,
        "description": "Rocket Money synced financial records including accounts, net worth, budgets, and transactions"
    }
    
    try:
        req = urllib.request.Request(
            kb_url, 
            data=json.dumps(kb_payload).encode("utf-8"),
            headers=headers,
            method="POST"
        )
        with urllib.request.urlopen(req, timeout=10) as response:
            pass
    except Exception:
        # Ignore failure if already exists (normally returns 400 or similar)
        pass

    # 2. Ingest the document
    ingest_url = f"http://{host}:{port}/api/v1/knowledge/{urllib.parse.quote(kb_name)}/documents"
    doc_payload = {
        "title": title,
        "source": "rocketmoney-sync",
        "mime_type": "text/markdown",
        "content": content
    }
    
    try:
        req = urllib.request.Request(
            ingest_url, 
            data=json.dumps(doc_payload).encode("utf-8"),
            headers=headers,
            method="POST"
        )
        with urllib.request.urlopen(req, timeout=30) as response:
            resp_data = json.loads(response.read().decode("utf-8"))
            return {"status": "success", "document_id": resp_data.get("id"), "title": title}
    except Exception as e:
        return {"status": "error", "error": str(e), "title": title}

if __name__ == "__main__":
    # Soulacy tool inputs are passed via stdin as JSON
    try:
        args = json.load(sys.stdin)
        title = args.get("title", "Financial Records")
        content = args.get("content", "")
        kb_name = args.get("kb_name", "financials")
        
        if not content:
            print(json.dumps({"error": "content is required"}))
            sys.exit(1)
            
        res = ingest_document(title, content, kb_name)
        print(json.dumps(res))
    except Exception as e:
        print(json.dumps({"error": str(e)}), file=sys.stderr)
        sys.exit(1)
