"""Attach correlation to the trusted local model hop, never a credential."""

from tools.approval_context import get_current_session_key


def correlate_model_request(request, base_url, **_):
    if str(base_url).rstrip("/") != "http://127.0.0.1:9917/v1":
        return None
    headers = dict(request.get("extra_headers") or {})
    headers["X-Hub-Hermes-Run-ID"] = get_current_session_key("")
    return {"request": {**request, "extra_headers": headers}}


def register(ctx):
    ctx.register_middleware("llm_request", correlate_model_request)
