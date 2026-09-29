---
description: "multi-consumer-credentials: проверки"
last_verified: "2026-09-28"
---

# Evidence

- `go test ./broker/ ./provider/ ./httpapi/ ./app/` — ok в golang:1.25-bookworm
  (windows build невозможен: securefs linux-only).
- docs-check: 57 markdown files passed.
- openapi drift check passed.
