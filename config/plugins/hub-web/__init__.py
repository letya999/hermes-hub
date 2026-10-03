"""Hub-managed multi-provider web search plugin.

Registered tools live in the ``web`` toolset so they inherit the same gate as
web_search/web_extract: the plugin is only mounted when the space's ``web``
feature is on, and ``agent.disabled_toolsets: [web]`` strips them from any
platform that would otherwise fall back to a composite toolset.

No provider API is reimplemented here: handlers reuse upstream's registered
WebSearchProvider objects from ``agent.web_search_registry`` so every engine
keeps its own auth, tiers and error semantics.
"""

from __future__ import annotations

from . import tools as _t  # noqa: F401


def register(ctx) -> None:
    """Register the hub web tools. Called once by the plugin loader."""
    _t.register_tools(ctx)
