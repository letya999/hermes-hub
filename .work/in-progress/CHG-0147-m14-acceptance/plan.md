# CHG-0147: Milestone 14 acceptance and connector lifecycle repair

Chain: ADR-0053 through ADR-0059, SPEC-0060 through SPEC-0063, and issues #281–#289, #104, #105 and #133. This record tracks the remaining acceptance after the original implementation CHGs were moved to done.

## Goal

Verify the milestone against the running local stack and two real team principals, repair concrete failures, and close only issues whose acceptance has evidence. Public HTTPS and remote-device acceptance stays in #283 outside this local-dev milestone.

## Slices

1. Verify installed GitHub, Workspace, Calendar, Playwright and other MCPs from current store, audit and Docker labels. No new provider consent is inferred from a green unit test.
2. Wire project- and connection-scoped Docker label query/purge into the served ToolHub endpoint. Test cross-project and connection-reuse denial before deployment.
3. Prove restart-safe finalizer resume, periodic orphan/credential/surface reconciliation and zero-resource receipts on a disposable stateful connector.
4. Exercise operation journal and source ledger order, Broker isolation/rotation, CI image provenance and install budgets with the relevant test and live gates.
5. Prove team connection lifecycle with the two owner-authorized Telegram principals, including distinct Broker grants and revoke behavior. Use an operator-reviewed shareable definition and a dedicated test credential or an explicitly chosen service.
6. Prove browser viewer login and profile purge; re-check natural-language disable/remove and OAuth isolation. Close each issue only when its listed criteria pass.

## Safety and gates

- No secrets, links, provider data or personal logs enter this record.
- Fail closed on ambiguous ownership. Never mutate unrelated connections during acceptance.
- Run focused red/green tests, `just check`, then the applicable local Docker gate; record any unavailable CI or public-device gate honestly.
