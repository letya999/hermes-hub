# SPEC-0037: Run artifact delivery to chat

Frozen: 2026-10-06. Issue 172.

1. `workspace/artifacts/documents`, `workspace/artifacts/images` and
   `workspace/artifacts/videos` are the
   deliverable area: hub tools place generated files there by contract. On a
   `completed` run the runtime lists regular, non-symlink files under that
   root modified inside the run window (mtime >= run start - 2 s), bounded
   to 8 files of <= 8 MiB each — <= 48 MiB under `videos`, below the 50 MB
   Telegram bot upload bound — and attaches `{name, path, mime, size}` refs
   to the terminal `ExecuteResponse`. Admission stamps a zero-byte
   `/state/runstarts/<runID>` marker whose mtime is the run's start bound;
   an observed/recovered run restores its window from that marker (markers
   sweep after ~24 h). A recovered run with no marker — admitted before the
   marker contract or in an environment without a state dir — still lists
   nothing by the window scan alone.
1a. Upstream `MEDIA:<path>` reply markers are a second, explicit source: the
   runtime strips marker lines from the visible text, resolves each path
   inside the workspace (EvalSymlinks containment, regular file, 0 < size <=
   8 MiB), stages it under `artifacts/images|documents` — copied there when a
   tool wrote it elsewhere, e.g. the workspace root — and attaches a ref.
   Bare filenames resolve against `artifacts/{images,documents,videos}`, and
   an absolute path that fails literal resolution but contains an
   `/artifacts/` segment is remapped onto the workspace artifacts root
   (models quote `$HOME/artifacts/...`), still under the same containment
   and size checks. A marker pointing at an unusable file yields a ref with
   `error`, never a silent drop. This source needs no run-start window, so
   recovered runs still deliver named files.
1b. The convention reaches the model through the managed run instructions —
   managed mode does not surface MCP `ServerOptions.Instructions` — so every
   managed prompt pins it: files under `artifacts/` deliver automatically on
   completion, and `MEDIA: <path>` attaches a file produced earlier or
   outside the deliverable root.

2. `POST /v1/artifact` on the runtime serves exactly one file by relative
   path: two segments, root ∈ {documents, images, videos}, clean basename, Lstat
   rejects symlinks and non-regular files, `EvalSymlinks` containment is
   re-verified, size within bounds. The supervisor owns the same path and
   forwards it to the runtime bound to the request envelope (lookup only, no
   lease — the gateway asks while the runtime is answering). No user home or
   workspace is mounted into the communication gateway.

3. The gateway fetches artifact bytes before persisting the terminal
   delivery receipt, staging them in `outbox/blobs/<delivery-file-id>/`.
   Each ref on the durable `Delivery` record carries either a `blob` name or
   an explicit `error`; a file that was generated but never crossed the
   contract is never silently dropped.

4. `deliverOne` sends artifacts after all text parts, in order, with
   `sent_artifacts` persisted per artifact — the same sent-boundary semantics
   as multi-part text. Telegram sends `image/png` and `image/jpeg` via
   `sendPhoto` with a `sendDocument` fallback, everything else via
   `sendDocument`. `video/*` mimes go via `sendVideo` with the same
   `sendDocument` fallback. Slack gets one explicit notice line per artifact until a
   Slack file-upload surface exists. `CompleteDelivery` removes the staged
   blob directory.
