"""Hub-managed multi-provider web search tools.

Two tools, registered into the ``web`` toolset:

- ``web_providers`` — list every web provider registered upstream, which are
  configured for this space's fan-out allowlist (``web.search_providers``),
  which is the default backend, and whether each is usable (keyed / keyless).
- ``web_search_multi`` — run one query against one or many configured
  providers in parallel and merge the hits. Results are tagged with the
  provider name, deduplicated by normalized URL (corroborating providers are
  listed on the kept entry) and ordered by best rank per provider order.
- ``web_cache`` — inspect, prune or clear this space's on-disk extract cache.
  Upstream TTL-gates reads but never deletes files, so pruning is the only
  way disk usage shrinks; search responses are in-memory and need nothing.

Provider objects come from upstream's ``agent.web_search_registry`` — no
vendor client is reimplemented here, so keys, tiers and honest vendor errors
stay inside the upstream providers.
"""

from __future__ import annotations

import logging
import time
from concurrent.futures import ThreadPoolExecutor, wait
from pathlib import Path
from typing import Any, Dict, List, Optional
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

from tools.registry import tool_error, tool_result

logger = logging.getLogger(__name__)

TOOLSET = "web"

_PROVIDER_LIMIT_CAP = 10   # per-provider result cap
_MERGED_CAP = 25           # merged output bound
_FANOUT_TIMEOUT_S = 60     # wall-clock bound for the whole fan-out
_MAX_FANOUT = 8            # parallel provider bound

_TRACKER_PARAMS = frozenset({
    "fbclid", "gclid", "dclid", "yclid", "mc_cid", "mc_eid", "igshid", "ref",
})

WEB_PROVIDERS_SCHEMA: Dict[str, Any] = {
    "type": "object",
    "properties": {},
    "description": (
        "Authoritative list of this space's web search providers: which engines "
        "are configured for parallel search, which one is the default, and each "
        "engine's live status (ready/keyless/unavailable). Call this before "
        "naming search engines to the user — never describe the provider set "
        "from memory or from other tools' descriptions."
    ),
}

WEB_SEARCH_MULTI_SCHEMA: Dict[str, Any] = {
    "type": "object",
    "properties": {
        "query": {"type": "string", "description": "The search query."},
        "providers": {
            "type": "array",
            "items": {"type": "string"},
            "description": (
                "Provider names to query in parallel (see web_providers for the "
                "configured set). Omit to use the configured default backend. "
                "Pass [\"all\"] to fan out to every configured provider."
            ),
        },
        "limit": {
            "type": "integer",
            "description": "Max results per provider (1-10, default 5).",
        },
    },
    "required": ["query"],
    "description": (
        "Search the web across one or more configured providers in parallel and "
        "merge deduplicated results. Use web_providers first if unsure which "
        "engines are configured; web_search stays the simple single-backend call."
    ),
}

WEB_CACHE_SCHEMA: Dict[str, Any] = {
    "type": "object",
    "properties": {
        "action": {
            "type": "string",
            "enum": ["status", "prune", "clear"],
            "description": (
                "status: report entry/file counts and size. prune: delete "
                "entries older than web.cache_ttl_minutes plus orphaned cache "
                "files. clear: wipe the entire extract cache."
            ),
        },
    },
    "required": ["action"],
    "description": (
        "Inspect or clean this space's on-disk web_extract page cache. Upstream "
        "caches every successful extraction under the user's own cache dir and "
        "skips expired entries on read, but never deletes them — prune/clear "
        "here is the only way disk usage shrinks. Search answers live in an "
        "in-memory cache only and need no cleanup."
    ),
}


def _web_config() -> dict:
    try:
        from hermes_cli.config import load_config
        web = (load_config() or {}).get("web")
        return web if isinstance(web, dict) else {}
    except Exception:
        return {}


def _norm_name(value: Any) -> str:
    return str(value or "").strip().lower()


def _default_backend(web: dict, key: str) -> str:
    return _norm_name(web.get(key)) or _norm_name(web.get("backend"))


def _allowed_providers(web: dict) -> List[str]:
    """Fan-out allowlist: ``web.search_providers`` plus the configured default
    (a default must always be callable). Order preserves the config list."""
    allowed: List[str] = []
    for value in web.get("search_providers") or []:
        name = _norm_name(value)
        if name and name not in allowed:
            allowed.append(name)
    default = _default_backend(web, "search_backend")
    if default and default not in allowed:
        allowed.insert(0, default)
    return allowed


def _registry():
    """Live upstream provider registry, or None when it cannot load."""
    try:
        from tools.web_tools import _ensure_web_plugins_loaded
        _ensure_web_plugins_loaded()
    except Exception:  # noqa: BLE001 — registry probe below still applies
        pass
    try:
        import agent.web_search_registry as registry
        return registry
    except Exception:  # noqa: BLE001
        return None


def _probe(provider: Any, method: str) -> bool:
    try:
        return bool(getattr(provider, method)())
    except Exception:  # noqa: BLE001 — a broken probe reads as unavailable
        return False


def _provider_status(provider: Any) -> str:
    """One-word state a model can quote verbatim without guessing."""
    if not _probe(provider, "supports_search") and not _probe(provider, "supports_extract"):
        return "unusable"
    if _probe(provider, "is_available"):
        return "ready"
    if _probe(provider, "is_keyless_available"):
        return "keyless"
    return "unavailable"


def _provider_map() -> Dict[str, Any]:
    """name -> provider object for every registered provider."""
    registry = _registry()
    if registry is None:
        return {}
    try:
        registered = registry.list_providers()
    except Exception:  # noqa: BLE001
        return {}
    out = {}
    for provider in registered:
        name = _norm_name(getattr(provider, "name", ""))
        if name:
            out[name] = provider
    return out


def _prompt_section(_session_info) -> str:
    """System-prompt grounding: the model must quote this list, not invent one.
    Rendered once per session from live config + provider probes."""
    web = _web_config()
    default = _default_backend(web, "search_backend") or "auto"
    allowed = _allowed_providers(web)
    lines = [
        "## Web search providers",
        f"Default engine (plain web_search): {default}.",
        f"Configured for parallel fan-out (web_search_multi): {', '.join(allowed) or 'none'}.",
    ]
    statuses = []
    for name in allowed:
        provider = _provider_map().get(name)
        statuses.append(f"{name}: {_provider_status(provider) if provider is not None else 'not registered'}")
    if statuses:
        lines.append("Current status: " + "; ".join(statuses) + ".")
    lines.append(
        "When asked which search engines exist or are active, answer only from "
        "this list or call web_providers for the live table — never invent names."
    )
    return "\n".join(lines)


def _norm_url(url: str) -> str:
    """Dedup key: scheme+host lowercased, www./trailing slash/fragment and
    tracker params dropped, query params sorted."""
    try:
        parts = urlsplit(url.strip())
    except Exception:  # noqa: BLE001
        return url.strip().lower()
    scheme = (parts.scheme or "https").lower()
    host = parts.netloc.lower()
    if host.startswith("www."):
        host = host[4:]
    path = parts.path.rstrip("/") or "/"
    query = urlencode(sorted(
        (k, v) for k, v in parse_qsl(parts.query, keep_blank_values=False)
        if not k.lower().startswith("utm_") and k.lower() not in _TRACKER_PARAMS
    ))
    return urlunsplit((scheme, host, path, query, "")).lower()


def _hits(payload: Any) -> List[Dict[str, Any]]:
    """Normalize one provider's search() payload to a hit list."""
    if not isinstance(payload, dict):
        return []
    rows = payload.get("data", {}).get("web") if isinstance(payload.get("data"), dict) else None
    if rows is None:
        rows = payload.get("results")
    if not isinstance(rows, list):
        return []
    hits = []
    for i, row in enumerate(rows):
        if not isinstance(row, dict):
            continue
        url = str(row.get("url") or "").strip()
        if not url:
            continue
        hits.append({
            "title": str(row.get("title") or ""),
            "url": url,
            "description": str(row.get("description") or row.get("snippet") or ""),
            "position": int(row.get("position") or i + 1),
        })
    return hits


def _search_one(name: str, query: str, limit: int) -> Dict[str, Any]:
    """Run one provider's search; never raises."""
    registry = _registry()
    provider = None
    if registry is not None:
        try:
            provider = registry.get_provider(name)
        except Exception:  # noqa: BLE001
            provider = None
    if provider is None:
        return {"provider": name, "ok": False, "error": "provider not registered"}
    if not _probe(provider, "supports_search"):
        return {"provider": name, "ok": False, "error": "provider does not support search"}
    try:
        payload = provider.search(query, limit)
    except Exception as exc:  # noqa: BLE001 — vendor error lands per-provider
        return {"provider": name, "ok": False, "error": f"{type(exc).__name__}: {exc}"}
    if not isinstance(payload, dict) or not payload.get("success"):
        error = payload.get("error") if isinstance(payload, dict) else None
        return {"provider": name, "ok": False, "error": str(error or "search failed")}
    return {"provider": name, "ok": True, "hits": _hits(payload)}


def handle_web_providers(args: dict, **kw) -> str:
    web = _web_config()
    registry = _registry()
    if registry is None:
        return tool_error("web provider registry unavailable")
    default_search = _default_backend(web, "search_backend")
    default_extract = _default_backend(web, "extract_backend")
    allowed = _allowed_providers(web)
    try:
        registered = registry.list_providers()
    except Exception as exc:  # noqa: BLE001
        return tool_error(f"web provider registry failed: {exc}")
    providers = []
    for provider in registered:
        name = _norm_name(getattr(provider, "name", ""))
        if not name:
            continue
        providers.append({
            "name": name,
            "status": _provider_status(provider),
            "supports_search": _probe(provider, "supports_search"),
            "supports_extract": _probe(provider, "supports_extract"),
            "configured_for_multi_search": name in allowed,
            "default_search": name == default_search,
            "default_extract": name == default_extract,
        })
    return tool_result({
        "success": True,
        "default_search_backend": default_search or "auto",
        "default_extract_backend": default_extract or "auto",
        "multi_search_providers": allowed,
        "providers": providers,
    })


def handle_web_search_multi(args: dict, **kw) -> str:
    query = str((args or {}).get("query") or "").strip()
    if not query:
        return tool_error("query is required")
    try:
        limit = min(max(int((args or {}).get("limit") or 5), 1), _PROVIDER_LIMIT_CAP)
    except (TypeError, ValueError):
        limit = 5
    web = _web_config()
    allowed = _allowed_providers(web)
    default = _default_backend(web, "search_backend")

    requested = (args or {}).get("providers")
    if requested is None:
        selected = [default] if default else []
    else:
        if isinstance(requested, str):
            requested = [requested]
        if not isinstance(requested, list):
            return tool_error("providers must be a list of provider names")
        names = [_norm_name(v) for v in requested]
        if any(n == "all" for n in names):
            selected = list(allowed)
        else:
            bad = [n for n in names if n and n not in allowed]
            if bad:
                return tool_error(
                    f"providers not configured for multi-search: {', '.join(bad)}; "
                    f"configured: {', '.join(allowed) or '(none)'}")
            selected = [n for n in names if n]
    if not selected:
        return tool_error("no search providers configured; set web.search_providers in the hub settings")

    # Pre-filter: configured-but-unusable providers (no key, missing package)
    # are reported as skipped without burning a request against them.
    known = _provider_map()
    skipped: Dict[str, str] = {}
    runnable: List[str] = []
    for name in selected:
        provider = known.get(name)
        if provider is None:
            skipped[name] = "provider not registered"
        elif not _probe(provider, "supports_search"):
            skipped[name] = "provider does not support search"
        elif not _probe(provider, "is_available") and not _probe(provider, "is_keyless_available"):
            skipped[name] = "provider unavailable (missing key, package or endpoint)"
        else:
            runnable.append(name)
    if not runnable:
        detail = "; ".join(f"{n}: {r}" for n, r in skipped.items())
        return tool_error(f"no configured provider is usable right now ({detail})")
    if len(runnable) > _MAX_FANOUT:
        runnable = runnable[:_MAX_FANOUT]
        for name in selected[_MAX_FANOUT:]:
            skipped[name] = f"fan-out cap {_MAX_FANOUT} reached"

    outcomes: Dict[str, Dict[str, Any]] = {}
    pool = ThreadPoolExecutor(max_workers=len(runnable), thread_name_prefix="web-multi")
    try:
        futures = {pool.submit(_search_one, name, query, limit): name for name in runnable}
        done, pending = wait(list(futures), timeout=_FANOUT_TIMEOUT_S)
        for future in pending:
            outcomes[futures[future]] = {"provider": futures[future], "ok": False,
                                         "error": f"timed out after {_FANOUT_TIMEOUT_S}s"}
        for future in done:
            try:
                outcomes[futures[future]] = future.result()
            except Exception as exc:  # noqa: BLE001 — defensive; _search_one never raises
                outcomes[futures[future]] = {"provider": futures[future], "ok": False,
                                             "error": f"{type(exc).__name__}: {exc}"}
    finally:
        # Never block the tool on a hung provider thread.
        pool.shutdown(wait=False, cancel_futures=True)

    merged: Dict[str, Dict[str, Any]] = {}
    errors: Dict[str, str] = {}
    succeeded: List[str] = []
    for order, name in enumerate(runnable):
        outcome = outcomes.get(name) or {"ok": False, "error": "no result"}
        if not outcome.get("ok"):
            errors[name] = str(outcome.get("error") or "search failed")
            continue
        succeeded.append(name)
        for rank, hit in enumerate(outcome.get("hits") or []):
            key = _norm_url(hit["url"])
            entry = merged.get(key)
            if entry is None:
                merged[key] = {**hit, "provider": name,
                               "providers": [name], "_sort": (rank, order)}
            else:
                if name not in entry["providers"]:
                    entry["providers"].append(name)
                entry["_sort"] = min(entry["_sort"], (rank, order))
    results = sorted(merged.values(), key=lambda e: e["_sort"])[:_MERGED_CAP]
    for entry in results:
        del entry["_sort"]

    return tool_result({
        "success": bool(succeeded),
        "query": query,
        "providers_used": succeeded,
        "providers_failed": errors,
        "providers_skipped": skipped,
        "partial_failure": (bool(errors) or bool(skipped)) and bool(succeeded),
        "results": results,
    })


def handle_web_cache(args: dict, **kw) -> str:
    action = str((args or {}).get("action") or "").strip().lower()
    if action not in ("status", "prune", "clear"):
        return tool_error("action must be status, prune or clear")
    # Upstream internals — Hermes is pinned, and every read/write below goes
    # through its own index helpers so the format stays upstream's.
    try:
        from tools import web_result_cache as wrc
        root = wrc._cache_dir()
        root = root.resolve() if root is not None else None
    except Exception:  # noqa: BLE001
        return tool_error("web extract cache module unavailable in this runtime")
    if root is None:
        return tool_result({"success": True, "action": action, "enabled": False,
                            "entries": 0, "files": 0, "bytes": 0,
                            "note": "extract cache unavailable in this runtime"})

    def cache_file(value: Any) -> Optional[Path]:
        """Resolve an index file path, refusing anything not flat inside root."""
        try:
            p = Path(str(value)).resolve()
        except Exception:  # noqa: BLE001
            return None
        return p if p.parent == root else None

    index = wrc._load_index()  # missing/corrupt index reads as empty
    files = [p for p in root.glob("*.cache.md") if p.is_file()]
    live = {p for e in index.values() if isinstance(e, dict)
            for p in [cache_file(e.get("file"))] if p is not None}
    def fsize(p: Path) -> int:
        try:
            return p.stat().st_size
        except OSError:
            return 0

    size = sum(fsize(p) for p in files)

    if action == "status":
        now = time.time()
        expired = sum(1 for e in index.values() if isinstance(e, dict)
                      and now - float(e.get("fetched_at", 0)) >= wrc.ttl_seconds())
        return tool_result({
            "success": True, "action": action,
            "cache_dir": str(root),
            "entries": len(index), "expired_entries": expired,
            "files": len(files), "orphan_files": len(files) - len(live & set(files)),
            "bytes": size,
            "ttl_minutes": wrc.ttl_seconds() / 60.0,
        })

    removed = 0
    if action == "prune":
        now, ttl = time.time(), wrc.ttl_seconds()
        before = len(index)
        for key, entry in list(index.items()):
            path = cache_file(entry.get("file")) if isinstance(entry, dict) else None
            if path is None or not path.exists() \
                    or now - float(entry.get("fetched_at", 0)) >= ttl:
                index.pop(key, None)
        kept = {p for e in index.values() if isinstance(e, dict)
                for p in [cache_file(e.get("file"))] if p is not None}
        for p in files:
            if p not in kept:
                try:
                    p.unlink()
                    removed += 1
                except OSError:
                    pass
        wrc._save_index(index)
        return tool_result({
            "success": True, "action": action,
            "entries_dropped": before - len(index),
            "entries_kept": len(index), "files_removed": removed,
            "files_kept": len(files) - removed,
        })

    for p in files:
        try:
            p.unlink()
            removed += 1
        except OSError:
            pass
    wrc._save_index({})
    return tool_result({
        "success": True, "action": action,
        "entries_dropped": len(index), "files_removed": removed,
    })


def register_tools(ctx) -> None:
    """Register the tools; called by the plugin loader's deferred path and by
    this package's ``register``."""
    ctx.register_tool(name="web_providers", toolset=TOOLSET, schema=WEB_PROVIDERS_SCHEMA,
                      handler=handle_web_providers, emoji="🛰️")
    ctx.register_tool(name="web_search_multi", toolset=TOOLSET, schema=WEB_SEARCH_MULTI_SCHEMA,
                      handler=handle_web_search_multi, emoji="🔎")
    ctx.register_tool(name="web_cache", toolset=TOOLSET, schema=WEB_CACHE_SCHEMA,
                      handler=handle_web_cache, emoji="🧹")
    # Ground the model in the configured provider set: without this section it
    # answers "what search do you have" from tool descriptions and memory, and
    # invents providers. The section is rendered per session from live config.
    ctx.register_system_prompt_section("hub-web-providers", _prompt_section)
