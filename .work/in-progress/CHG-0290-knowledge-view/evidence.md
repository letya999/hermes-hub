# CHG-0290 local evidence — 2026-11-08

Source HEAD: `185892107fcc6056a41808fe22533a6c34c7940e` plus uncommitted
CHG-0290 changes. No remote acceptance or published image is claimed.

- `memory_open` is in immutable platform catalog 20 and
  `hub-agent-tools@18.0.2`; docs, prompt guidance and locale catalogs changed
  together. Final `just check` and affected package tests passed, including
  the ToolHub security test, migration selection and hubctl doctor contract.
- One scoped `hubctl dev-rebuild ... toolhub` replaced only the existing
  artem_main ToolHub container. Running image identity:
  `sha256:83abaff7c5b20027e04a0990885acc3a6dd435d23d2c78c501a94c4ae89d45da`.
- `space apply` published catalog 20 for existing artem_main and artem_two dev
  spaces, then respawned managed runtimes. Active revisions:
  `r-8763bd7f30b86faa` and `r-c6ad0b9808836f2d`. Both apply journals are
  absent. The host-side supervisor needed a temporary loopback-only bridge;
  it was removed after both applies. An initial failed apply was recovered by
  the documented journal/replay path before publication.
- The 18.0.1 catalog's 48 unchanged capability tuples were copied to the
  18.0.2 digest for each owner's policy ceiling and profile allows, preserving
  actions, resources, limits and scopes. Host policy/profile preview and
  confirmation paths admitted both updates. This manual digest re-pin is
  required for each catalog version until it has a migration path.
- Current projection resolves `memory_open`, `memory_search` and `file_list`
  for both owners at 18.0.2. No new memory right was issued.
- Two artem_two mock-channel model runs called `memory_open`; ToolHub audit
  has admitted and completed `memory.open` events with `outcome=ok` and
  `execution=succeeded` for both exact jobs. Each final model delivery carried
  a link. Both links opened ToolHub `/knowledge` with HTTP 200 and HTML.
  Four exact mock deliveries and the one created chat link were deleted.
- Isolated security test covers private-run minting, group/completed-run
  denial, search/read, no write route, forged link, current right and
  selection revocation, published memory disable, one-hour expiry and
  non-loopback TLS origin restriction. It does not substitute for remote CI.

The link is a one-hour bearer. ToolHub rechecks current rights on each page
request. The page is read-only and does not expose the upstream OpenKnowledge
editor or its direct filesystem access.
