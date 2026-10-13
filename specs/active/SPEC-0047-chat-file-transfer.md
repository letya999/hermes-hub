# SPEC-0047: Chat file transfer in both directions

Frozen: 2026-10-10. Issues 150/151 follow-up — chat file exchange as a general
capability; zip archives and multi-file batches are instances, not the limit.

## Inbound: user → hub → workspace

1. A channel attachment (Telegram document/photo today) is staged by the
   communication gateway before any workspace sees it: metadata under
   `spool/inbound/<id>.json`, payload under `spool/inbound/<id>.bin`. The
   gateway holds no workspace mount, so this quarantine lives outside it.
   The record carries owner principal, conversation, original name, mime and
   size, and survives gateway restarts until consumed.
2. The job text carries a metadata line `[file name=<n> mime=<m> size=<s>
   inbox=<id>]` after the user's caption, so the model can reference the file.
3. The tool plane reaches the payload only through control endpoints
   `GET /v1/inbound/<id>` and `POST /v1/inbound/<id>/consume`: bearer control
   auth, the resolved principal must equal the record owner — foreign
   principals get 403, unknown ones 401. `GET` returns
   `application/octet-stream` plus `X-Hub-Name` with the original filename.
4. `inbox_fetch` materializes a staged file into `workspace/inbox/`. A zip is
   unpacked from bytes in memory — the archive itself is never written into
   the workspace — under `inbox/<name>/`, reusing the archive safety contract:
   no traversal/absolute/symlink/encrypted/non-regular entries, bounded entry
   count and decompressed bytes. Archives with more than 8 files are
   materialized as one directory group, never as per-file chat attachments.
   On success the staged record is consumed (payload and metadata deleted);
   on failure it stays staged for inspection or retry.
5. The managed-run instructions teach the model the `inbox=<id>` →
   `inbox_fetch` flow, because managed runtimes do not surface MCP server
   instructions.

## Outbound: workspace → hub → user

1. Deliverables keep living under `workspace/artifacts/{documents,images,
   videos}` per SPEC-0037; a top-level `artifacts/*.zip` is also a valid
   deliverable with mime `application/zip`.
2. `POST /v1/artifact` accepts `consume`. When the fetching channel requests
   consumption (Telegram sets `Consume: true`), the runtime deletes the
   workspace file only after the response body copied successfully — a
   failed fetch never loses the source.
3. "Delivered means gone" is the default for generated files the user asked
   to receive: the workspace copy is removed on handoff. Nothing is kept
   solely as a delivery intermediate — the durable copy while sending lives
   in `spool/outbox/blobs/` and is removed by `CompleteDelivery`.
