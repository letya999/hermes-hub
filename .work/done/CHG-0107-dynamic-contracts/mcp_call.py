import re
import json, sys, urllib.request

BASE = "http://127.0.0.1:8090/mcp"
TOKEN = "40db6874eae22d34ce337c50cae9e6de25f266ef96edeec942b9cebd0889d3a2"

def post(payload, session=None):
    req = urllib.request.Request(BASE, data=json.dumps(payload).encode(), method="POST")
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    if session:
        req.add_header("Mcp-Session-Id", session)
    r = urllib.request.urlopen(req, timeout=300)
    sid = r.headers.get("Mcp-Session-Id")
    body = r.read().decode()
    # streamable http may answer SSE
    if "data:" in body:
        for line in body.splitlines():
            if line.startswith("data:"):
                body = line[5:].strip()
    return sid, json.loads(body) if body else {}

sid, _ = post({"jsonrpc": "2.0", "id": 1, "method": "initialize",
               "params": {"protocolVersion": "2025-03-26",
                          "capabilities": {},
                          "clientInfo": {"name": "chg0107-verify", "version": "0"}}})
post({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}}, sid)

tool = sys.argv[1]
args = json.loads(sys.argv[2]) if len(sys.argv) > 2 else {}
sid, resp = post({"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                  "params": {"name": tool, "arguments": args}}, sid)
sc = resp.get("result", {}).get("structuredContent")
if sc is None and resp.get("result", {}).get("content"):
    m = re.search(r"\{.*\}", resp["result"]["content"][0]["text"], re.S)
    sc = json.loads(m.group(0)) if m else resp["result"]["content"][0]["text"]
if isinstance(sc, dict):
    keys = ("phase", "status", "next_action", "nonce", "error", "binding_id",
            "broker_request_id", "broker_contract_id", "broker_contract_revision",
            "authorization_url", "credentials", "instructions", "tools_count")
    print(json.dumps({k: sc[k] for k in keys if k in sc}, indent=1, ensure_ascii=False))
else:
    print(json.dumps(sc if sc is not None else resp, indent=1, ensure_ascii=False)[:8000])
