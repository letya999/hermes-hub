"""Drive toolhub control ops over the live MCP endpoint for CHG-0105 replay.

Usage: python mcp_replay.py <op> [json-args]
Reads HUB_RUNTIME_AUTH from spaces/local/runtime.auth; never prints the token.
"""
import json
import sys
import urllib.request
import urllib.error

ENDPOINT = "http://127.0.0.1:8090/mcp"
AUTH_FILE = "spaces/local/runtime.auth"


def token():
    for line in open(AUTH_FILE, encoding="utf-8"):
        if line.startswith("HUB_RUNTIME_AUTH="):
            return line.split("=", 1)[1].strip()
    sys.exit("no HUB_RUNTIME_AUTH in " + AUTH_FILE)


def post(payload, session=None):
    body = json.dumps(payload).encode()
    req = urllib.request.Request(ENDPOINT, data=body, method="POST")
    req.add_header("Authorization", "Bearer " + token())
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    if session:
        req.add_header("Mcp-Session-Id", session)
    try:
        res = urllib.request.urlopen(req, timeout=600)
    except urllib.error.HTTPError as e:
        return session, {"http_error": e.code, "body": e.read().decode()[:2000]}
    sid = res.headers.get("Mcp-Session-Id", session)
    raw = res.read().decode()
    if res.headers.get("Content-Type", "").startswith("text/event-stream"):
        for line in raw.splitlines():
            if line.startswith("data:"):
                return sid, json.loads(line[5:].strip())
        return sid, {"sse_no_data": raw[:500]}
    return sid, json.loads(raw) if raw.strip() else {}


def main():
    op = sys.argv[1]
    args = json.loads(sys.argv[2]) if len(sys.argv) > 2 else {}
    sid, res = post({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"protocolVersion": "2025-03-26", "capabilities": {},
                   "clientInfo": {"name": "chg0105-replay", "version": "1"}},
    })
    if "result" not in res:
        print(json.dumps(res, indent=1)); sys.exit(1)
    post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)
    sid, res = post({
        "jsonrpc": "2.0", "id": 2, "method": "tools/call",
        "params": {"name": op, "arguments": args},
    }, sid)
    print(json.dumps(res.get("result", res), indent=1, ensure_ascii=False)[:6000])


if __name__ == "__main__":
    main()
