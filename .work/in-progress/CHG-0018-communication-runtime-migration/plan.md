# M1 communication runtime migration (#19)

1. Verify current #16-18 repository and pinned-Hermes Docker gates before closure.
2. Persist host-selected per-context execution mode and explicit cron disposition.
3. Audit stopped gateway spool; block claimed/admitted work, preserve queued and uncertain jobs/delivery receipts without replay.
4. Share static/supervised environment and mount contracts; bind gateway identity per user.
5. Verify the complete gateway-to-real-Hermes path for two users, duplicate input, warm reuse, restart and automatic five-minute idle shutdown.
6. Document compatibility rollout/rollback and deletion gates; run just check and Docker gates.

Legacy deletion remains gated on one compatibility release and accepted real runtime evidence. No live accounts or external sending are needed for isolated Docker verification.
