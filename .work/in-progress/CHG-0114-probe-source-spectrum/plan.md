# CHG-0114: Probe source spectrum and delivery modes

Chain: ADR-0043 (amended) -> SPEC-0057 amendment -> this plan -> state.yaml.
Issue: GitHub #248 extended scope — any owner-granted source as a probe,
content/artifact delivery, hub-mediated tool needs for scripts, opt-in
render and registered service endpoints (job_ftch et al).

## Goal

A routine's probe is `source + extract[] + compare`; the source spectrum is
http | tool(alias mcp_tool) | script | command | render | service | task.
Delivery is notify | content | artifact. Probe scripts stay networkless but
may declare needs[] — tool calls the hub prefetches under owner grants.

## Slices

- S1 tool alias: kind=tool normalizes to mcp_tool; description/docs teach
  that any projected read tool works (file_read, knowledge_search,
  web_search connectors, job_ftch-mcp).
- S2 needs[]: WatchProbe.Needs ≤4 {tool,args}; probeOne prefetches through
  the toolCall path; ScriptRequest.Needs rides argv to the container;
  builtin needs() returns the list.
- S3 delivery modes: Delivery=content sends the value every successful
  tick; artifact stages the value as a text attachment through
  WriteDeliveryBlob.
- S4 kind=render: HUB_RENDER_URL contract POST {url, steps[], wait_for} →
  {html}; declarative steps (click/fill/scroll/wait_for/wait_ms/evaluate);
  extract chain applies to the rendered DOM.
- S5 kind=service: HUB_SERVICE_ENDPOINTS name→URL registry; POST args →
  {text}; internal endpoints never reachable via raw probe.url.

## Rules

Read-effect only through /v1/tool-call (ToolHub already enforces).
kind=service names resolve only from operator config, never request text.
Render stays out of the base image. No write-effect probes.
