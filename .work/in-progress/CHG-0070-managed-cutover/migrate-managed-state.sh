#!/usr/bin/env bash
# migrate-managed-state.sh <space-dir> <env>
# Copies legacy space state into the managed/<env>/ layout the supervisor
# bind-mounts. Control-plane material (toolhub store, credential store,
# controller keys, runtime.json pid ledger) is deliberately NOT copied:
# it never belonged to the agent and managed runtimes must not see it.
# Extension roots stay empty — managed preflight rejects non-empty ones
# and mounts them read-only tmpfs anyway.
# NB: cp + prune, not tar|tar — Windows bsdtar chokes on MSYS paths in -C.
set -euo pipefail
SPACE=$(cd "$1" && pwd); ENV=$2
DST="$SPACE/managed/$ENV"
mkdir -p "$DST"/{runtime,hermes,home,cache,workspace}

cp -a "$SPACE/hermes/." "$DST/hermes/"
# Transient gateway process state and any env-file secrets do not carry over.
rm -f "$DST/hermes"/{auth.lock,gateway.lock,gateway.pid,gateway.sock,gateway-starts.log,.env,.op.env}
# Sealed extension roots: recreated empty.
rm -rf "$DST/hermes"/{bin,hooks,plugins,skills,skill-bundles,scripts,node,lsp}
for d in skills hooks plugins skill-bundles scripts bin node lsp; do
  mkdir -p "$DST/hermes/$d"
done

[ -d "$SPACE/home" ] && cp -a "$SPACE/home/." "$DST/home/" || true
[ -d "$SPACE/cache" ] && cp -a "$SPACE/cache/." "$DST/cache/" || true
[ -d "$SPACE/workspace" ] && cp -a "$SPACE/workspace/." "$DST/workspace/" || true

# /state root: only agent-facing dirs; skip toolhub/, credentials/,
# credential.key, generic-controller.*, runtime.json (control plane).
for d in artifacts browser google telegram materialized; do
  [ -d "$SPACE/runtime/$d" ] && cp -a "$SPACE/runtime/$d" "$DST/runtime/$d" || mkdir -p "$DST/runtime/$d"
done
echo "migrated -> $DST"
find "$DST" -maxdepth 2 -type d | sort
