"""Pinned-upstream integration fixture, launched only by the Go devcheck driver.

No user mounts, external network, real credentials, upstream patches or mocked
Hermes dispatch. A loopback HTTP model fixture deliberately emits forbidden calls.
This proves the selected admission paths, not container escape resistance or the
future ToolHub policy. JSON output contains only synthetic evidence and inventory.
"""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PIN = sys.argv[1]
assert Path("/opt/hermes/.git/HEAD").read_text().strip() == PIN, "wrong upstream pin"
home = Path(os.environ["HERMES_HOME"])
home.mkdir(parents=True, exist_ok=True)
Path(os.environ["HOME"]).mkdir(parents=True, exist_ok=True)
canary = Path("/tmp/capability-canary")
canary.write_text("unchanged")

import yaml
from tools.registry import discover_builtin_tools, registry
from toolsets import TOOLSETS, resolve_toolset
from hermes_cli.tools_config import _get_platform_tools
from model_tools import get_tool_definitions
from gateway.config import Platform, PlatformConfig

imported_modules = discover_builtin_tools()
registered = sorted(registry.get_all_tool_names())
members = {name: sorted(resolve_toolset(name)) for name in sorted(TOOLSETS)}
inventory = {
    "registered": registered,
    "entries": {entry.name: {
        "toolset": entry.toolset, "schema": entry.schema,
        "requires_env": entry.requires_env,
        "conditional": entry.check_fn is not None,
        "dynamic_schema": entry.dynamic_schema_overrides is not None,
    } for entry in registry.get_all_entries()},
    "aliases": registry.get_registered_toolset_aliases(),
    "imported_modules": sorted(imported_modules),
    # Hash source even when optional module imports fail. A registry-only diff
    # cannot detect a conditional registration in an unimportable module.
    "tool_sources": {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                     for path in sorted(Path("/opt/hermes/tools").glob("*.py"))},
    "toolsets": members,
    "declared": sorted({tool for group in members.values() for tool in group}),
}
assert "terminal" in registered and "write_file" in registered
platforms = sorted({"cli", "cron", "subagent"} | {item.value for item in Platform})
disabled = sorted(set(TOOLSETS) | {registry.get_toolset_for_tool(name) for name in registered} - {None})
config = json.loads(sys.argv[2])
assert sorted(config["platform_toolsets"]) == platforms, "rendered platform inventory drift"
assert all(not config["platform_toolsets"][platform] for platform in platforms), "native platform enabled"
assert sorted(config["agent"]["disabled_toolsets"]) == disabled, "rendered disabled-toolset inventory drift"
assert not config["mcp_servers"] and config["plugins"]["enabled"] == [], "unreviewed extensions"
assert config["memory"] == {"memory_enabled": False, "user_profile_enabled": False}
assert config["stt"]["enabled"] is False and config["security"]["allow_lazy_installs"] is False

# A valid on-disk hook executes at discovery despite hooks: {} in YAML. The
# managed runtime must therefore mount an empty, host-validated hook root.
hook_dir = home / "hooks" / "synthetic_probe"
hook_dir.mkdir(parents=True)
(hook_dir / "HOOK.yaml").write_text("name: synthetic_probe\nevents: [gateway:startup]\n")
hook_canary = Path("/tmp/hook-canary")
(hook_dir / "handler.py").write_text(
    "from pathlib import Path\nPath('/tmp/hook-canary').write_text('executed')\n"
    "def handle(event_type, context):\n    return None\n"
)
from gateway.hooks import HookRegistry
HookRegistry().discover_and_load()
assert hook_canary.read_text() == "executed", "on-disk hook startup path changed"
(hook_dir / "handler.py").unlink()
(hook_dir / "HOOK.yaml").unlink()
hook_dir.rmdir()
hook_canary.unlink()


def definitions(cfg, platform):
    groups = sorted(_get_platform_tools(cfg, platform))
    tools = get_tool_definitions(
        enabled_toolsets=groups,
        disabled_toolsets=cfg.get("agent", {}).get("disabled_toolsets", []),
        quiet_mode=True,
    )
    return groups, [tool["function"]["name"] for tool in tools]


# The negative control catches a broken probe that always reports no tools.
fallback = _get_platform_tools({"platform_toolsets": {"cli": []}}, "api_server")
assert "terminal" in fallback and "file" in fallback, "negative control failed"
empty_surfaces = {}
for platform in platforms:
    groups, names = definitions(config, platform)
    assert not groups and not names, (platform, groups, names)
    empty_surfaces[platform] = names

# MCP is independent of a native platform list; sentinel suppression is tested
# without making a network connection to the synthetic URL.
mcp_config = {"platform_toolsets": {"cli": []}, "mcp_servers": {"probe": {"url": "http://example.invalid/mcp"}}}
assert "probe" in _get_platform_tools(mcp_config, "cli")
mcp_config["platform_toolsets"]["cli"] = ["no_mcp"]
assert "probe" not in _get_platform_tools(mcp_config, "cli")

# Source/registry inventory must include registered tools that would be invisible
# because their prerequisites are absent. This fixture tool never has a handler
# side effect and is not enabled in the test agent.
registry.register(
    name="probe_unavailable", toolset="probe_optional",
    schema={"name": "probe_unavailable", "description": "Synthetic unavailable tool", "parameters": {"type": "object"}},
    handler=lambda **kwargs: "unexpected", check_fn=lambda: False,
)
assert "probe_unavailable" in registry.get_all_tool_names()
assert not get_tool_definitions(enabled_toolsets=["probe_optional"], quiet_mode=True)
assert definitions(config, "api_server")[1] == []
registry.register(
    name="probe_new_available", toolset="probe_new_group",
    schema={"name": "probe_new_available", "description": "Synthetic new tool", "parameters": {"type": "object"}},
    handler=lambda **kwargs: canary.write_text("changed"), check_fn=lambda: True,
)
assert get_tool_definitions(enabled_toolsets=["probe_new_group"], quiet_mode=True), "new-tool negative control failed"
assert definitions(config, "api_server")[1] == [], "a new group widened the zero profile"

requests_seen = []
probe_message = "Respond to this synthetic authorization probe."
forbidden = [
    ("terminal", {"command": "printf changed > /tmp/capability-canary"}),
    ("write_file", {"path": str(canary), "content": "changed"}),
    ("skill_manage", {"action": "create", "name": "probe", "content": "synthetic"}),
    ("tool_call", {"name": "write_file", "arguments": {"path": str(canary), "content": "changed"}}),
    ("probe_new_available", {}),
]


class Model(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        is_probe = any(message.get("role") == "user" and message.get("content") == probe_message
                       for message in request.get("messages", []))
        if is_probe:
            requests_seen.append(request)
        tool_results = {message.get("tool_call_id") for message in request.get("messages", [])
                        if message.get("role") == "tool"}
        # Housekeeping calls and transport retries can carry the same user
        # message. Keep emitting calls until real tool results arrive; counting
        # HTTP requests would let such a retry skip the dispatch proof entirely.
        if is_probe and not all("forbidden-" + str(index) in tool_results for index in range(len(forbidden))):
            message = {"role": "assistant", "content": None, "tool_calls": [
                {"id": "forbidden-" + str(index), "type": "function", "function": {"name": name, "arguments": json.dumps(args)}}
                for index, (name, args) in enumerate(forbidden)
            ]}
            finish = "tool_calls"
        else:
            message = {"role": "assistant", "content": "CAPABILITY_PROBE_DONE"}
            finish = "stop"
        body = {"id": "probe-response", "model": "capability-probe", "created": 1}
        self.send_response(200)
        if request.get("stream"):
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for index, call in enumerate(message.get("tool_calls", [])):
                call["index"] = index
            body.update(object="chat.completion.chunk", choices=[{"index": 0, "delta": message, "finish_reason": finish}])
            self.wfile.write(("data: " + json.dumps(body) + "\n\ndata: [DONE]\n\n").encode())
        else:
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            body.update(object="chat.completion", choices=[{"index": 0, "message": message, "finish_reason": finish}])
            self.wfile.write(json.dumps(body).encode())


server = ThreadingHTTPServer(("127.0.0.1", 0), Model)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
config["model"] = {"provider": "custom", "default": "capability-probe", "base_url": "http://127.0.0.1:" + str(server.server_port) + "/v1"}
(home / "config.yaml").write_text(yaml.safe_dump(config))
os.environ["OPENAI_API_KEY"] = "synthetic-probe-credential"
os.environ["OPENAI_BASE_URL"] = config["model"]["base_url"]


def assert_rejections(result):
    assert len(requests_seen) >= 2, "the model fixture did not exercise the rejection round"
    assert all(not request.get("tools") for request in requests_seen), "unapproved outgoing tool schema"
    assert canary.read_text() == "unchanged", "forbidden file/terminal call executed"
    errors = {message.get("tool_call_id"): message.get("content")
              for request in requests_seen for message in request.get("messages", [])
              if message.get("role") == "tool"}
    for index, (name, _args) in enumerate(forbidden):
        assert errors.get("forbidden-" + str(index), "").strip() == "Tool '" + name + "' does not exist. Available tools:", "missing exact rejection evidence for " + name
    assert "CAPABILITY_PROBE_DONE" in json.dumps(result), "conversation failed before completion"


def api_request(path, data=None, authenticated=True):
    headers = {"Content-Type": "application/json"}
    if authenticated:
        headers["Authorization"] = "Bearer synthetic-capability-api-credential"
    request = Request("http://127.0.0.1:9125" + path, headers=headers,
                      data=json.dumps(data).encode() if data is not None else None)
    with urlopen(request, timeout=10) as response:
        return response.status, json.load(response)


gateway = None
gateway_log = None
try:
    from gateway.platforms.api_server import APIServerAdapter
    adapter = APIServerAdapter(PlatformConfig())
    agent = adapter._create_agent(session_id="capability-probe")
    assert agent.tools == [] and agent.valid_tool_names == set(), "API construction widened zero profile"
    result = agent.run_conversation(probe_message)
    assert_rejections(result)
    agent_request_count = len(requests_seen)
    requests_seen.clear()

    # Exercise the same HTTP admission route used by hub-runtime in a separate
    # native process. The Go container boundary still forbids external network.
    gateway_log = Path("/tmp/gateway.log").open("w+")
    gateway = subprocess.Popen(
        ["hermes", "gateway", "run", "--no-supervise", "--force"],
        stdout=gateway_log, stderr=subprocess.STDOUT,
        env=dict(os.environ, API_SERVER_ENABLED="true", API_SERVER_HOST="127.0.0.1",
                 API_SERVER_PORT="9125", API_SERVER_KEY="synthetic-capability-api-credential",
                 HERMES_GATEWAY_NO_TTY="true"),
    )
    deadline = time.monotonic() + 60
    while True:
        assert gateway.poll() is None, "native gateway exited before admission"
        try:
            status, _ = api_request("/health")
            if status == 200:
                break
        except (HTTPError, URLError, TimeoutError):
            pass
        assert time.monotonic() < deadline, "native API health deadline"
        time.sleep(0.2)
    run_body = {"input": probe_message, "session_id": "capability-http", "provider": "custom", "model": "capability-probe"}
    try:
        api_request("/v1/runs", run_body, authenticated=False)
        raise AssertionError("unauthenticated run admitted")
    except HTTPError as error:
        assert error.code == 401, "unexpected unauthenticated run response"
    status, _ = api_request("/api/sessions", {"id": "capability-http", "title": "Synthetic capability probe"})
    assert status == 201, "synthetic session creation failed"
    status, admission = api_request("/v1/runs", run_body)
    assert status == 202 and admission.get("run_id"), "native run admission failed"
    deadline = time.monotonic() + 60
    while True:
        status, result = api_request("/v1/runs/" + admission["run_id"])
        assert status == 200, "native run lookup failed"
        if result.get("status") in {"completed", "failed", "cancelled", "interrupted"}:
            break
        assert time.monotonic() < deadline, "native run completion deadline"
        time.sleep(0.2)
    assert result.get("status") == "completed", "native zero-profile run failed: " + str(result.get("status"))
    assert_rejections(result)
finally:
    if gateway is not None:
        gateway.terminate()
        try:
            gateway.wait(timeout=10)
        except subprocess.TimeoutExpired:
            gateway.kill()
            gateway.wait(timeout=5)
    if gateway_log is not None:
        if sys.exc_info()[0] is not None:
            gateway_log.seek(0)
            print("NATIVE_GATEWAY_DIAGNOSTICS=" + gateway_log.read()[-2000:], file=sys.stderr)
        gateway_log.close()
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)

encoded = json.dumps(inventory, sort_keys=True, separators=(",", ":")).encode()
print("CAPABILITY_REPORT=" + json.dumps({
    "schema": 1,
    "pin": PIN,
    "inventory_sha256": hashlib.sha256(encoded).hexdigest(),
    "inventory": inventory,
    "empty_surfaces": empty_surfaces,
    "api_agent_forged_calls_rejected": [name for name, _ in forbidden],
    "api_http_forged_calls_rejected": [name for name, _ in forbidden],
    "agent_fixture_requests": agent_request_count,
    "http_fixture_requests": len(requests_seen),
    "canary_unchanged": True,
    "disk_hook_import_executed": True,
    "limits": ["No live provider", "CLI/channel/cron/child coverage is resolver-only", "No mutable-grant revocation or OS isolation proof"],
}, sort_keys=True))
