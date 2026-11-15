# CHG-0290: Temporary ToolHub knowledge view

## Goal

Let the agent open a private read-only knowledge page by issuing a link that
expires after one hour. Keep ToolHub as the only reader/writer of personal
memory; the upstream OpenKnowledge editor remains deferred.

## Scope

- Add `memory_open` to the immutable agent-tool catalog and project it
  through current selection, `memory.search` and exact private-run admission.
- Serve `/knowledge` on the existing human-action listener with a signed
  one-hour link; recheck current selection, grant, published memory switch and
  owner vault for every search/read request.
- Document the UI boundary, update the prompt contract and test expiry,
  tampering, group denial and live revocation.

## Acceptance

- Synthetic direct tool call mints a one-hour link only in a live private run.
- Link search/read stays owner-only; revocation/disable/expiry stop access.
- No upstream service or direct vault mount is reintroduced.
- Current catalog release emitted immutably; managed rebind and respawn
  demonstrated on an existing dev space when safe.

## Non-goals

OpenKnowledge upstream editor, memory mutation through the page, account
login, public publishing, and changes to personal vault contents.
