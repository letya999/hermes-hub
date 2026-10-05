package agenttools

import (
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// HubToolsDefinitionID is the single in-process tool catalog the managed
// ToolHub projects for a runtime. There is no remote source: execution is the
// private per-call executor (hubctl tools-exec) inside the owning runtime.
const HubToolsDefinitionID = "hub-agent-tools"

// HubToolsDefinition returns the reviewed agent-tools catalog: one ToolSpec
// per handler with the exact capability tuple it consumes. The operator
// selects tools (with capability, implementation digest, path prefixes and
// limits) into capability profiles; nothing here is reachable without a
// selection.
func HubToolsDefinition() toolhub.ToolDefinition {
	arg := func(name, typ string, required bool) toolhub.CLIArgument {
		return toolhub.CLIArgument{Name: name, Type: typ, Required: required}
	}
	path := arg("path", "string", true)
	str := func(name string) toolhub.CLIArgument { return arg(name, "string", true) }
	opt := func(name string) toolhub.CLIArgument { return arg(name, "string", false) }
	use := func(action, resource string, args ...string) []toolhub.CapabilityUse {
		if len(args) > 0 {
			return []toolhub.CapabilityUse{{Action: action, Resource: resource, PathArgument: args[0]}}
		}
		return []toolhub.CapabilityUse{{Action: action, Resource: resource}}
	}
	tool := func(name, capability, description string, effect toolhub.Effect, uses []toolhub.CapabilityUse, arguments ...toolhub.CLIArgument) toolhub.ToolSpec {
		return toolhub.ToolSpec{Name: name, Effect: effect, CapabilityID: capability, Description: description, Uses: uses, Arguments: arguments}
	}
	return toolhub.ToolDefinition{
		Schema:       toolhub.SchemaVersion,
		DefinitionID: HubToolsDefinitionID,
		Version:      "1.1.0",
		Transport:    toolhub.AgentTools,
		Source:       toolhub.DefinitionSource{},
		Workload: toolhub.WorkloadPolicy{
			Class:     toolhub.PerUser,
			Rationale: "in-process executor inside the owning runtime; per-call os.Root scoping",
		},
		Execution: toolhub.ExecutionPolicy{
			TimeoutSeconds: 120,
			OutputBytes:    2 << 20,
			CPUMillis:      500,
			MemoryMiB:      256,
			MaxPIDs:        32,
			// Declared egress contract for provider-facing tools. The executor
			// runs inside the agent network where egress is relay-bounded; SSH
			// endpoints come from the operator-reviewed ssh capability config.
			Egress: []string{"api.hh.ru", "communication-hub"},
		},
		Health: toolhub.HealthProbe{Kind: "exec", Value: "hubctl", TimeoutSeconds: 5},
		Tools: []toolhub.ToolSpec{
			tool("file_list", "files.list", "List files inside the admitted workspace scope.", toolhub.ReadEffect, use("list", "files", "path"), opt("root"), opt("path")),
			tool("file_read", "files.read", "Read one file inside the admitted workspace scope.", toolhub.ReadEffect, use("read", "files", "path"), opt("root"), path),
			tool("file_write", "files.write", "Create or update one file inside the admitted workspace scope; revision-guarded.", toolhub.WriteEffect, use("write", "files", "path"), opt("root"), path, str("text"), opt("revision")),
			tool("file_search", "files.search", "Search file contents inside the admitted workspace scope.", toolhub.ReadEffect, use("search", "files", "path"), opt("root"), opt("path"), str("query")),
			tool("service_catalog", "services.read", "List optional services available on this runtime.", toolhub.ReadEffect, use("read", "services")),
			tool("service_enable", "services.enable", "Enable one self-service integration declared in the catalog.", toolhub.WriteEffect, use("enable", "services"), str("service")),
			tool("service_disable", "services.disable", "Disable one self-service integration declared in the catalog.", toolhub.WriteEffect, use("disable", "services"), str("service")),
			// Runtime self-settings: the same admitted/audited discipline as
			// service_* — a reviewed key allowlist, typed values, atomic file,
			// restart-scheduled application. No arbitrary config writes.
			tool("settings_get", "settings.read", "Show runtime self-setting overrides: the manageable key allowlist, current overrides and their effective values. Secret values are never shown.", toolhub.ReadEffect, use("read", "settings"), opt("key")),
			tool("settings_set", "settings.write", "Set one manageable runtime setting or reset it to the rendered default. Applies after the scheduled runtime restart.", toolhub.WriteEffect, use("write", "settings"), str("key"), opt("value")),
			tool("routine_create", "routines.create", "Create a scheduled routine on this runtime.", toolhub.WriteEffect, use("create", "routines"), str("id"), str("timezone"), str("expression"), str("text")),
			tool("routine_list", "routines.list", "List routines configured on this runtime.", toolhub.ReadEffect, use("list", "routines")),
			tool("routine_update", "routines.update", "Update a routine schedule or text.", toolhub.WriteEffect, use("update", "routines"), str("id"), opt("expression"), opt("text")),
			tool("routine_pause", "routines.pause", "Pause a routine.", toolhub.WriteEffect, use("update", "routines"), str("id")),
			tool("routine_delete", "routines.delete", "Delete a routine.", toolhub.WriteEffect, use("delete", "routines"), str("id")),
			tool("document_extract", "documents.read", "Extract text from a document inside the admitted scope.", toolhub.ReadEffect, use("read", "documents", "path"), path),
			tool("document_create", "documents.create", "Create a document artifact from supplied text.", toolhub.WriteEffect, use("create", "documents"), str("name"), str("format"), str("text")),
			tool("document_edit", "documents.edit", "Replace the text of a document artifact inside the admitted scope.", toolhub.WriteEffect, use("edit", "documents", "path"), path, str("text")),
			tool("document_convert", "documents.convert", "Convert a document inside the admitted scope into a new artifact.", toolhub.WriteEffect, append(use("convert", "documents", "path"), use("create", "artifacts")...), path, str("name"), str("format")),
			tool("image_inspect", "images.read", "Inspect an image inside the admitted scope.", toolhub.ReadEffect, use("read", "images", "path"), path),
			tool("image_convert", "images.convert", "Convert an image inside the admitted scope into a new artifact.", toolhub.WriteEffect, append(use("convert", "images", "path"), use("create", "artifacts")...), str("name"), path, str("format")),
			tool("artifact_remove", "artifacts.delete", "Remove one artifact inside the admitted scope.", toolhub.WriteEffect, use("delete", "artifacts", "path"), path),
			tool("image_generate", "image_gen.generate", "Generate an image with the granted provider.", toolhub.WriteEffect, use("generate", "images"), str("name"), str("prompt")),
			tool("image_edit", "image_gen.edit", "Edit an image inside the admitted scope with the granted provider.", toolhub.WriteEffect, use("edit", "images", "path"), str("name"), path, str("prompt")),
			{
				Name:         "code_exec",
				CapabilityID: "terminal.exec",
				Effect:       toolhub.WriteEffect,
				Sandboxed:    true,
				Description:  "Run a bounded script in a disposable container: read-only inputs from the admitted scope, private scratch, no network; exported files land under the scope.",
				Uses:         append(append(use("exec", "terminal"), use("read", "files", "path")...), use("write", "files", "path")...),
				Arguments: []toolhub.CLIArgument{
					arg("command", "string", true), arg("path", "string", true),
				},
			},
			tool("hh_search", "hh.search", "Search HeadHunter vacancies for the authorized query.", toolhub.ReadEffect, use("read", "hh"), str("query"), toolhub.CLIArgument{Name: "page", Type: "integer"}),
			tool("hh_vacancy", "hh.vacancy", "Read one HeadHunter vacancy.", toolhub.ReadEffect, use("read", "hh"), str("id")),
			tool("hh_resumes", "hh.resumes", "List the owner resumes on HeadHunter.", toolhub.ReadEffect, use("read", "hh")),
			tool("hh_apply", "hh.apply", "Apply to one HeadHunter vacancy with the reviewed resume and message.", toolhub.WriteEffect, use("apply", "hh"), str("id"), str("resume_id"), str("message"), toolhub.CLIArgument{Name: "authorized", Type: "boolean", Required: true}),
			tool("ssh_hosts", "ssh.list", "List SSH hosts declared in the reviewed capability config.", toolhub.ReadEffect, use("list", "ssh")),
			tool("ssh_exec", "ssh.exec", "Run a bounded command on a declared SSH host.", toolhub.WriteEffect, use("exec", "ssh"), str("host"), str("command")),
			tool("ssh_read", "ssh.read", "Read a remote file on a declared SSH host.", toolhub.ReadEffect, use("read", "ssh"), str("host"), path),
			tool("ssh_write", "ssh.write", "Write a remote file on a declared SSH host when the write grant is active.", toolhub.WriteEffect, use("write", "ssh"), str("host"), path, str("text")),
			tool("ssh_shell_open", "ssh.shell", "Open an interactive shell on a declared SSH host when the shell grant is active.", toolhub.WriteEffect, use("shell", "ssh"), str("host")),
			tool("ssh_shell_send", "ssh.shell_send", "Send input to an open SSH shell.", toolhub.WriteEffect, use("shell", "ssh"), str("id"), str("data")),
			tool("ssh_shell_read", "ssh.shell_read", "Read output from an open SSH shell.", toolhub.ReadEffect, use("shell", "ssh"), str("id"), toolhub.CLIArgument{Name: "offset", Type: "integer"}),
			tool("ssh_shell_close", "ssh.shell_close", "Close an open SSH shell.", toolhub.WriteEffect, use("shell", "ssh"), str("id")),
			tool("ssh_tunnel_open", "ssh.tunnel", "Open a tunnel through a declared SSH host when the tunnel grant is active.", toolhub.WriteEffect, use("tunnel", "ssh"), str("host"), str("name")),
			tool("ssh_tunnel_list", "ssh.tunnel_list", "List open SSH tunnels.", toolhub.ReadEffect, use("tunnel", "ssh")),
			tool("ssh_tunnel_close", "ssh.tunnel_close", "Close an SSH tunnel.", toolhub.WriteEffect, use("tunnel", "ssh"), str("id")),
		},
	}
}
