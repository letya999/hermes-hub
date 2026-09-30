"""Provision a Viewer service account in Grafana and write its token to /secrets."""

import base64
import json
import os
import time
import urllib.error
import urllib.request

G = os.environ["GRAFANA_URL"]
AUTH = b"Basic " + base64.b64encode((os.environ["GRAFANA_USER"] + ":" + os.environ["GRAFANA_PASSWORD"]).encode())


def call(method, path, body=None):
    req = urllib.request.Request(G + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": AUTH, "Content-Type": "application/json"})
    try:
        return json.loads(urllib.request.urlopen(req, timeout=10).read())
    except urllib.error.HTTPError as e:
        if e.code in (400, 409):
            return None
        raise


while True:
    try:
        urllib.request.urlopen(G + "/api/health", timeout=5)
        break
    except Exception:
        time.sleep(2)

sa = call("POST", "/api/serviceaccounts", {"name": "toolhub-mcp", "role": "Viewer", "isDisabled": False})
if sa is None:
    sa = next(a for a in call("GET", "/api/serviceaccounts/search?query=toolhub-mcp")["serviceAccounts"]
              if a["name"] == "toolhub-mcp")
    if os.path.exists("/secrets/grafana-sat.txt"):
        raise SystemExit("service account %s already provisioned" % sa["id"])
token = call("POST", f"/api/serviceaccounts/{sa['id']}/tokens", {"name": "mcp", "secondsToLive": 86400 * 30})
with open("/secrets/grafana-sat.txt", "w") as f:
    f.write(token["key"])
print("service account", sa["id"], "token written to /secrets/grafana-sat.txt", flush=True)
