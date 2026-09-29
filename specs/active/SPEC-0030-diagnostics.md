# SPEC-0030: Local multiuser diagnostics

Frozen: 2026-09-26.

1. An operator can export retained logs from every Hermes Docker container,
   across user spaces and services, into one local Git-ignored text snapshot.
   Export is explicit and replaces the previous snapshot.
2. Accepted ordinary Telegram messages are logged with user, job and message
   identifiers after authorization and durable enqueue. Unknown senders,
   intercepted secrets and credential commands do not emit message bodies.
3. Job completion and ToolHub audit metadata carry job/run correlation without
   logging provider credentials or MCP request bodies.
4. Docker logs rotate with a 30 MB uncompressed ceiling per container. The
   diagnostic export reads Docker's retained logs; it does not create another
   unbounded continuous log stream.
