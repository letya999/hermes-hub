"""Random-ish demo API: diverse routes, latency and failures for telemetry."""

import json
import os
import random
import time
from datetime import datetime, timezone

from fastapi import FastAPI, Request
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest
from starlette.responses import Response

app = FastAPI(title="mockapi")
REQS = Counter("http_requests_total", "HTTP requests", ["method", "route", "status"])
LAT = Histogram("http_request_duration_seconds", "HTTP latency", ["route"],
                buckets=[0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5])
LOG = open(os.environ.get("ACCESS_LOG", "/logs/mockapi.jsonl"), "a", buffering=1)

USERS = {i: {"id": i, "name": f"user{i}", "role": random.choice(["admin", "user", "viewer"])}
         for i in range(1, 51)}
PRODUCTS = [{"sku": f"SKU-{i:03d}", "name": random.choice(["widget", "gadget", "sensor", "panel"]) + f"-{i}",
             "price": round(random.uniform(3, 900), 2), "stock": random.randint(0, 500)} for i in range(1, 121)]
ORDERS = {}


@app.middleware("http")
async def observe(req: Request, call_next):
    start = time.time()
    resp = await call_next(req)
    dt = time.time() - start
    route = req.url.path
    REQS.labels(req.method, route, resp.status_code).inc()
    LAT.labels(route).observe(dt)
    LOG.write(json.dumps({"@timestamp": datetime.now(timezone.utc).isoformat(), "method": req.method,
                          "path": route, "status": resp.status_code, "latency_ms": round(dt * 1000, 1),
                          "client": req.client.host if req.client else ""}) + "\n")
    return resp


@app.get("/api/health")
def health():
    return {"status": "ok"}


@app.get("/api/users")
def users(role: str | None = None, limit: int = 20):
    rows = list(USERS.values())
    if role:
        rows = [u for u in rows if u["role"] == role]
    return rows[: max(1, min(limit, 100))]


@app.get("/api/users/{uid}")
def user(uid: int):
    if uid not in USERS:
        return Response(json.dumps({"error": "not found"}), 404, media_type="application/json")
    return USERS[uid]


@app.get("/api/products")
def products(q: str = "", limit: int = 10):
    rows = [p for p in PRODUCTS if q.lower() in p["name"]] if q else PRODUCTS
    return rows[: max(1, min(limit, 100))]


@app.post("/api/orders", status_code=201)
def order(body: dict):
    if not body.get("sku") or not isinstance(body.get("qty"), int) or body["qty"] < 1:
        return Response(json.dumps({"error": "sku and positive int qty required"}), 422,
                        media_type="application/json")
    oid = len(ORDERS) + 1
    ORDERS[oid] = {"id": oid, "sku": body["sku"], "qty": body["qty"],
                   "status": random.choice(["new", "paid", "shipped"])}
    return ORDERS[oid]


@app.get("/api/orders/{oid}")
def get_order(oid: int):
    if oid not in ORDERS:
        return Response(json.dumps({"error": "not found"}), 404, media_type="application/json")
    return ORDERS[oid]


@app.get("/api/stats")
def stats():
    return {"users": len(USERS), "products": len(PRODUCTS), "orders": len(ORDERS)}


@app.get("/api/flaky")
def flaky():
    if random.random() < 0.3:
        return Response(json.dumps({"error": "upstream exploded"}), 502, media_type="application/json")
    return {"ok": True}


@app.get("/api/slow")
def slow():
    time.sleep(random.uniform(0.2, 3.0))
    return {"ok": True}


@app.get("/metrics")
def metrics():
    return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)
