"""Pin-sensitive self-check of Hermes' real breaker bookkeeping, no provider calls."""
import ast
import json
import pathlib
import sys
import types

source = ast.parse(pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else '/opt/hermes/tools/mcp_tool_handlers.py').read_text())
strikes, resets = [], []
scope = {
    '_core': types.SimpleNamespace(_bump_server_error=strikes.append, _reset_server_error=resets.append),
    '_loop': types.SimpleNamespace(_run_on_mcp_loop=lambda call, timeout: call()),
    '_sanitize_error': str, '_exc_str': str,
    'tool_error': lambda message: json.dumps({'error': message}),
}
for name in ('_record_call_outcome', '_dispatch'):
    node = next(n for n in source.body if isinstance(n, ast.FunctionDef) and n.name == name)
    exec('from __future__ import annotations\n' + ast.unparse(node), scope)

def dispatch(call):
    return scope['_dispatch']('toolhub', object(), 'tools/call', call, 10, (),
                              lambda exc: strikes.append('toolhub'), record_outcome=True)

for _ in range(4):
    assert json.loads(dispatch(lambda: '{"error":"missing file"}')) == {'error': 'missing file'}
assert not strikes and len(resets) == 4, 'tool failures tripped the transport breaker'

def disconnected():
    raise ConnectionError('transport disconnected')

assert 'transport disconnected' in json.loads(dispatch(disconnected))['error']
assert strikes == ['toolhub'], 'a transport failure did not strike'
print('MCP tool-error/transport breaker self-check passed')
