# CHG-0051: config-gated empty tools/list (mcp-atlassian)

## Problem (observed live)

`onboard-b30fb395ea0bd67f0df38966` (sooperset/mcp-atlassian) built fine but
failed preflight with `MCP tools/list empty: <nil>: <stderr>` — the server
answers the protocol with a valid response carrying **zero tools** until
connection config exists. Verified on the built image:

- no env → `tools: 0`
- `JIRA_URL/JIRA_USERNAME/JIRA_API_TOKEN/CONFLUENCE_* = preflight` → `tools: 96`

Two gaps prevented the normal awaiting-credentials path:

1. `connectionFieldFromEnv` dropped every non-secret env entry that carried a
   value, so `JIRA_URL=https://your-company.atlassian.net` never became an
   input — even though it is the author's replace-me marker and the server is
   dead without it. The preflight placeholder probe only injects `Required`
   inputs, so mcp-atlassian saw no config and listed nothing.
2. `credentialGateFrom` only gated on authentication errors that name
   secret-shaped variables; an answered-empty `tools/list` was a hard review
   failure.

## Fix

- `connectionFieldFromEnv` (`recipe_resolver.go`): a non-secret `NAME=` line
  whose value is a placeholder (`your-*`, `example.*`, `changeme`, `xxx`,
  `*_here`, `TODO`, `dummy`, `<…>` …) is collected as a **required** input;
  real defaults like `GITLAB_API_URL=https://gitlab.com` are still skipped.
  Applies to `.env.example` and compose env parsing alike.
- `artifact_preflight.go`: the answered-empty tools/list error is marked with
  the `errEmptyToolList` sentinel (a process that died before answering does
  not carry it). `credentialGateFrom` falls back to `emptyToolsCredentialNames`
  — all declared `Required` inputs — when the auth-error classifier finds no
  names, so the review asks for the declared contract instead of failing.
  `emptyToolsCredentialGroups` synthesizes either/or alternatives by name
  prefix (`JIRA_*` vs `CONFLUENCE_*`) so one product suite satisfies the gate.
- `control.go` `acceptCredentialGate`/`credentialGateHints`: the gate now
  renders every required input — not only secrets — because a config-gated
  server still needs its URLs/usernames, and admission requires non-grouped
  required names to be collectable. Secret names keep the password widget;
  non-secret required inputs render as text.

## Failure semantics preserved

- Empty `tools/list` with **no required inputs** → still a review failure.
- Process that crashed/EOF'd before answering → still a review failure (the
  sentinel only marks an answered-empty response).
- Optional inputs never join the gate.

## Tests

- `TestParseExampleEnvTreatsPlaceholderValuesAsRequiredInputs`
- `TestCredentialGateFromAnsweredEmptyToolList` (gate + prefix groups + hint
  typing; EOF and no-required-inputs negatives)
- existing `TestParseExampleEnvKeepsCredentialsAndSkipsRuntimeDefaults` and the
  slack gate test pin the unchanged behaviour.
