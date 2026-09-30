---
id: CHG-0056-observability-connectors
issue: 43
---
# Prepared observability connectors: grafana + prometheus (issue #43)

## Decision

Two exact-source prepared entries on the generic ToolHub lifecycle, plus a
throwaway local observability stack used to prove the end-to-end path:

- `grafana` — `grafana/mcp-grafana` (Go, Apache-2.0) @45cfa5d (v1.6.2). Service
  account token contract: `GRAFANA_URL` + `GRAFANA_SERVICE_ACCOUNT_TOKEN`,
  optional `GRAFANA_ORG_ID` pins every call to one org (server-side header,
  outside model arguments). Fixed envs `GRAFANA_USAGE_STATS=disabled` and
  `GRAFANA_LOKI_GUARDRAIL_MODE=enforce` bound query cost.
- `prometheus` — `prometheus/prometheus-mcp` (Go, Apache-2.0) @cd0e552.
  URL-only contract (`PROMETHEUS_MCP_SERVER_PROMETHEUS_URL`); fixed envs pin
  `DOCS_AUTO_UPDATE=false` (no unreviewed docs egress) and a default
  `TRUNCATION_LIMIT=500` response bound. Prometheus itself has no auth;
  authenticated fronts (basic-auth proxy) are a later contract revision via the
  upstream `--http.config` file delivery.

## Connectivity

Workload networks are `--internal`; all egress crosses the workload Squid
CONNECT proxy whose ACL is definition-time. HTTPS `*_URL` credential values add
their host to the ACL (real deployments: grafana.net etc. work with URL alone).
Plain-HTTP endpoints get the reviewed `tcp_forwards` mechanism instead: a
non-secret `*_ENDPOINT` (`host:port`) field joins the ACL and drives the
companion loopback listener, and the provider URL points at
`127.0.0.1:<forward>` — identical to the SQL connector model. Catalog egress
sentinel stays `127.0.0.1`: every real destination is credential-derived.

Tool effects come from upstream MCP annotations (`readOnlyHint` /
`destructiveHint`): reads/searches classify `read`, dashboard/datasource
administration `write` — a separately granted effect. Both entries gate the
projected toolset at review time: grafana's catalog argv extends the inferred
entrypoint with `--enabled-tools=<read categories>` + `--disable-write` (25
read tools admitted, upstream strips the mutation verbs from the manage_*
tools), prometheus pins `MCP_SERVER_MCP_TOOLS` to the read set (12 tools,
no TSDB admin). Org scoping via `GRAFANA_ORG_ID` is enforced by the server
header, folder/datasource boundaries by the service account's RBAC.

## Demo stack (docker/observability, issue #43 acceptance fixture)

Plain compose: prometheus (scrapes mockapi /metrics), elasticsearch 8
(single-node, security off — private obs network only), grafana 12.2 with
provisioned Prometheus+Elasticsearch datasources and one dashboard, FastAPI
mockapi with ~10 varied endpoints + a spammer producing diverse traffic and a
JSONL access log shipped to Elasticsearch, plus a one-shot bootstrap that
creates a Viewer service account and writes its token to
`docker/observability/.secrets/` (gitignored). Ports publish on loopback:
grafana 3300, prometheus 9090; workloads reach them through
`host.docker.internal:<port>` carried in the credential `*_ENDPOINT` fields.

## Non-goals / deferred evidence

No loki/kibana/logstash replicas — Elasticsearch + Grafana cover the evidence
needs. No write-path demonstration beyond effect classification; provider
writes stay a separate granted action. No public-network exposure.
