"""Endless broad traffic against the mock API. Stdlib only."""

import json
import os
import random
import time
import urllib.request

T = os.environ.get("TARGET", "http://localhost:8000")


def hit(method, path, body=None):
    req = urllib.request.Request(T + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json"})
    try:
        urllib.request.urlopen(req, timeout=10).read()
    except Exception:
        pass  # 4xx/5xx are traffic too


def main():
    words = ["widget", "gadget", "sensor", "panel", "nope"]
    while True:
        pick = random.random()
        if pick < 0.25:
            hit("GET", f"/api/users?limit={random.randint(1, 100)}"
                       + (f"&role={random.choice(['admin', 'user', 'viewer', 'ghost'])}" if random.random() < 0.4 else ""))
        elif pick < 0.45:
            hit("GET", f"/api/users/{random.randint(1, 70)}")
        elif pick < 0.6:
            hit("GET", f"/api/products?q={random.choice(words)}&limit={random.randint(1, 50)}")
        elif pick < 0.75:
            ok = random.random() < 0.7
            hit("POST", "/api/orders", {"sku": f"SKU-{random.randint(1, 130):03d}", "qty": random.randint(1, 9)}
                if ok else random.choice([{}, {"sku": "SKU-001"}, {"sku": "SKU-001", "qty": -2}, {"qty": "x"}]))
        elif pick < 0.85:
            hit("GET", f"/api/orders/{random.randint(1, 200)}")
        elif pick < 0.92:
            hit("GET", "/api/flaky")
        elif pick < 0.97:
            hit("GET", "/api/slow")
        else:
            hit("GET", f"/api/unknown-{random.randint(1, 9)}")
        time.sleep(random.choice([0.05, 0.1, 0.2, 0.5, 1.0]) * (5 if random.random() < 0.05 else 1))


if __name__ == "__main__":
    main()
