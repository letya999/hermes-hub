package stack

// okReadTools is the read-only MCP surface verified against the pinned
// @inkeep/open-knowledge@0.88.2 bundle (dist/*.mjs registerTool calls). It is
// rendered as tools.include on the runtime's mcp_servers entry, so excluded
// verbs are never registered with the runtime — the projection layer drops
// them rather than merely hiding them from the model.
//
// `lint` stays out although its default mode is read: its `fix` argument
// mutates documents and tools.include filters by tool name, not arguments.
// `exec` stays in: upstream restricts it to a read-only shell allowlist
// (cat/ls/grep/find/head/tail/wc/sort/uniq/cut) inside the OK container.
var okReadTools = []string{
	"audit", "config", "exec", "history", "links", "palette", "search", "skills",
}

// okMutationTools are the 0.88.2 verbs that write vault content or produce
// external side effects. `checkpoint` and `share_link` are here although
// upstream flags them readOnlyHint: a checkpoint persists to git history and
// a share link mints a publishing URL — both are effects the read grant must
// not produce. `preview_url` is excluded too: a cold call auto-starts the OK
// server, a demand side effect rather than a read.
var okMutationTools = []string{
	"checkpoint", "delete", "edit", "import", "install", "lint", "move",
	"preview_url", "restore_version", "share_link", "write",
}

// okServer renders the hub-owned OpenKnowledge endpoint for the runtime's
// mcp_servers map. The service name resolves on the space-private network
// (R8.3) and PORT is pinned by the compose environment so the URL is stable.
// On a supervisor-mode unmanaged space the runtime lives on the shared hub
// net, where the service publishes the space-scoped alias the compose
// render attaches — identical contract, collision-free name.
func okServer(s Settings) M {
	return M{"url": "http://" + okServiceHost(s) + ":8080/mcp", "timeout": 120, "skip_preflight": true, "tools": MCPTools{Include: okReadTools}}
}

// okServiceHost is the resolvable name peers dial: the plain service name
// on space-private nets, the space-scoped shared-net alias on
// supervisor-mode unmanaged stacks.
func okServiceHost(s Settings) string {
	if s.CapabilityMode != "managed" && s.ExecutionMode == "supervisor" {
		return "open-knowledge-" + s.User
	}
	return "open-knowledge"
}
