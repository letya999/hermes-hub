# CHG-0231: MCP onboarding contract and failure repair

## Goal

Existing Broker ledgers accept new reviewed contract revisions; failed enrollment cannot leave a live form; a digest conflict is explicit at binding.

## Changes

- Bump all 13 localized prepared contracts and catalog pins; pin each `id@revision` digest in the Go gate.
- Compare prepared digests during binding and report Broker registration conflicts.
- Fail the active onboarding and clear its form nonce after any fatal post-persist error.

## Verification

- `TestPreparedContractDigests`, `TestBindPreparedDigestConflictIsExplicit`, `TestPreparePostPersistBrokerFailureRevokesForm`.
- `just check`; Broker and live connector acceptance remain separate gates.
