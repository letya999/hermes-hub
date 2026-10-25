# CHG-0060 Artifact delivery to chat (issue #172)

## Goal

Files generated under `workspace/artifacts/{documents,images}` during a run
reach the originating Telegram chat as documents/photos; other channels get an
explicit, non-silent notice. No user-home mounts into the gateway; bytes cross
the private runtime HTTP contract only while the runtime is answering the job.

## Design

1. Runtime: on run admission record `runStart`; on `completed` status scan the
   artifacts root for regular files modified inside the run window
   (mtime >= runStart - 2s skew), bounded to 8 files of <=8 MiB each.
   Emit `artifacts` refs on the terminal `ExecuteResponse`.
2. Runtime: `POST /v1/artifact` `{name}` serves one artifact file after strict
   path validation (documents|images segment, basename only, no symlinks,
   EvalSymlinks containment, size cap).
3. Supervisor: `POST /v1/artifact` forwards to the running runtime bound to the
   request envelope (lookup only; no lease — the runtime is mid-answer).
4. Gateway: on the terminal `completed` event fetch each artifact into
   `outbox/blobs/<delivery-file-id>/`; store `{name,mime,size,blob|error}` on
   `Delivery.Artifacts`. Failed fetches become explicit `error` entries —
   never silent.
5. `deliverOne`: after text parts, send artifacts in order; durable
   `SentArtifacts` progress; Telegram images (png/jpeg) via sendPhoto with
   sendDocument fallback, everything else via sendDocument; Slack gets a
   notice line per artifact until Slack upload exists.
6. `CompleteDelivery` removes the delivery's blob directory.

## Deferred

- Slack `files.getUploadURLExternal` upload (notice-only for now).
- Attribution beyond the run window (parallel contexts share one workspace).
- Voice/other artifact kinds beyond documents+images.
