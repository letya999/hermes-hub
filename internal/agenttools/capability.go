package agenttools

import (
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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
func HubToolsDefinition() api.ToolDefinition {
	arg := func(name, typ string, required bool) api.CLIArgument {
		return api.CLIArgument{Name: name, Type: typ, Required: required}
	}
	path := arg("path", "string", true)
	str := func(name string) api.CLIArgument { return arg(name, "string", true) }
	opt := func(name string) api.CLIArgument { return arg(name, "string", false) }
	use := func(action, resource string, args ...string) []api.CapabilityUse {
		if len(args) > 0 {
			return []api.CapabilityUse{{Action: action, Resource: resource, PathArgument: args[0]}}
		}
		return []api.CapabilityUse{{Action: action, Resource: resource}}
	}
	tool := func(name, capability, description string, effect api.Effect, uses []api.CapabilityUse, arguments ...api.CLIArgument) api.ToolSpec {
		return api.ToolSpec{Name: name, Effect: effect, CapabilityID: capability, Description: description, Uses: uses, Arguments: arguments}
	}
	return api.ToolDefinition{
		Schema:       api.SchemaVersion,
		DefinitionID: HubToolsDefinitionID,
		Version:      "5.5.0",
		Transport:    api.AgentTools,
		Source:       api.DefinitionSource{},
		Workload: api.WorkloadPolicy{
			Class:     api.PerUser,
			Rationale: "in-process executor inside the owning runtime; per-call os.Root scoping",
		},
		Execution: api.ExecutionPolicy{
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
		Health: api.HealthProbe{Kind: "exec", Value: "hubctl", TimeoutSeconds: 5},
		Tools: []api.ToolSpec{
			tool("file_list", "files.list", "List files inside the admitted workspace scope.", api.ReadEffect, use("list", "files", "path"), opt("root"), opt("path")),
			tool("file_read", "files.read", "Read one file inside the admitted workspace scope.", api.ReadEffect, use("read", "files", "path"), opt("root"), path),
			tool("file_write", "files.write", "Create or update one file inside the admitted workspace scope; revision-guarded.", api.WriteEffect, use("write", "files", "path"), opt("root"), path, str("text"), opt("revision")),
			tool("file_search", "files.search", "Search file contents inside the admitted workspace scope.", api.ReadEffect, use("search", "files", "path"), opt("root"), opt("path"), str("query")),
			tool("service_catalog", "services.read", "List optional services available on this runtime.", api.ReadEffect, use("read", "services")),
			tool("service_enable", "services.enable", "Enable one self-service integration declared in the catalog.", api.WriteEffect, use("enable", "services"), str("service")),
			tool("service_disable", "services.disable", "Disable one self-service integration declared in the catalog.", api.WriteEffect, use("disable", "services"), str("service")),
			// Runtime self-settings: the same admitted/audited discipline as
			// service_* — a reviewed key allowlist, typed values, atomic file,
			// restart-scheduled application. No arbitrary config writes.
			tool("settings_get", "settings.read", "Show runtime self-setting overrides: the manageable key allowlist, current overrides and their effective values. Secret values are never shown.", api.ReadEffect, use("read", "settings"), opt("key")),
			tool("context_status", "context.read", "Show this session's context control state: effective threshold, journaled compaction count and last event, live token estimate. Unknown is null.", api.ReadEffect, use("read", "context"), opt("id"), opt("context")),
			tool("context_compact", "context.compact", "Compact this session's transcript at a turn boundary: prune tool outputs, summarize the head, keep a verbatim tail. A busy session refuses deterministically unless queue parks the request. Result is metadata only.", api.WriteEffect, use("compact", "context"), opt("id"), opt("context"), opt("focus"), opt("engine"), arg("here", "integer", false), arg("prune", "boolean", false), arg("preview", "boolean", false), arg("queue", "boolean", false)),
			tool("goal_set", "goals.set", "Set or replace this conversation's persistent goal. The goal keeps working across turns with verified completion and a bounded turn budget.", api.WriteEffect, use("set", "goals"), str("text")),
			tool("goal_status", "goals.read", "Show this conversation's goal record: status, turns used against the budget, last verdict and bounded reason.", api.ReadEffect, use("read", "goals")),
			tool("goal_pause", "goals.pause", "Pause this conversation's goal: queued continuations no-op until resume.", api.WriteEffect, use("pause", "goals")),
			tool("goal_resume", "goals.resume", "Resume a paused goal and queue its next turn.", api.WriteEffect, use("resume", "goals")),
			tool("goal_clear", "goals.delete", "Clear this conversation's goal record entirely.", api.WriteEffect, use("delete", "goals")),
			tool("settings_set", "settings.write", "Set one manageable runtime setting or reset it to the rendered default. Applies after the scheduled runtime restart.", api.WriteEffect, use("write", "settings"), str("key"), opt("value")),
			tool("routine_create", "routines.create", routineDescriptions["routine_create"], api.WriteEffect, use("create", "routines"), str("id"), str("timezone"), str("expression"), str("text"), opt("origin_job_id"), opt("destination_task_id"), opt("kind"), opt("url"), opt("regex"), opt("expect"), opt("probe_kind"), opt("tool"), opt("args"), opt("service"), opt("steps"), opt("wait_for"), opt("needs"), opt("recipe"), opt("extract"), opt("probe_script"), opt("deliver_mode"), opt("artifact_name"), opt("notify_mode"), opt("quiet_from"), opt("quiet_to"), arg("daily_limit", "integer", false), arg("max_retries", "integer", false), arg("source_ids", "array", false), arg("timing", "object", false)),
			tool("routine_list", "routines.list", routineDescriptions["routine_list"], api.ReadEffect, use("list", "routines"), arg("limit", "integer", false), opt("cursor"), opt("kind"), opt("state"), opt("query"), opt("view")),
			tool("routine_get", "routines.list", routineDescriptions["routine_get"], api.ReadEffect, use("list", "routines"), str("id"), arg("include", "array", false), arg("history_limit", "integer", false), opt("history_cursor"), arg("preview_limit", "integer", false)),
			tool("routine_edit", "routines.update", routineDescriptions["routine_edit"], api.WriteEffect, use("update", "routines"), str("id"), str("action"), arg("expected_revision", "integer", true), arg("patch", "object", false), opt("until"), opt("origin_job_id")),
			tool("routine_delete", "routines.delete", routineDescriptions["routine_delete"], api.WriteEffect, use("delete", "routines"), str("id"), arg("expected_revision", "integer", true)),
			tool("watch_subscribe", "watches.create", routineDescriptions["watch_subscribe"], api.WriteEffect, use("create", "watches"), str("id"), str("timezone"), str("expression"), str("text"), str("tool"), opt("sink"), opt("args")),
			tool("watch_list", "watches.list", routineDescriptions["watch_list"], api.ReadEffect, use("list", "watches")),
			tool("watch_pause", "watches.pause", routineDescriptions["watch_pause"], api.WriteEffect, use("update", "watches"), str("id")),
			tool("watch_delete", "watches.delete", routineDescriptions["watch_delete"], api.WriteEffect, use("delete", "watches"), str("id")),
			// Standing assignments (SPEC-0080 R2.3): the agent plans and
			// steers; the owner alone widens grants via chat authorize —
			// no authorize argument exists on any assignment tool.
			tool("assignment_plan", "assignments.plan", "Dry-run an assignment plan before creating: pass plan as a JSON object {sources:[{name,schedule}],specialist:{instructions,reply}}; returns per-source verdicts, persists nothing. Always dry-run a new or changed plan first.", api.ReadEffect, use("plan", "assignments"), str("plan")),
			tool("assignment_create", "assignments.create", "Create a standing assignment from a verbatim user intent (text). Optional title, plan (JSON) and queue=true to provision sources immediately. The assignment survives restarts; deterministic probes run before any model wake — unchanged sources never wake the specialist. Grants bound LLM spend (default daily wake cap 20).", api.WriteEffect, use("create", "assignments"), str("text"), opt("title"), opt("plan"), arg("queue", "boolean", false)),
			tool("assignment_list", "assignments.list", "List the owner's standing assignments: id, title, state (draft|active|waiting|paused|completed|cancelled|failed), blocked reason, durable counters.", api.ReadEffect, use("list", "assignments"), arg("limit", "integer", false), opt("cursor")),
			tool("assignment_get", "assignments.list", "Read one assignment card: state, applied schedule receipts, plan revision, grants, checkpoint counters, blocked reason.", api.ReadEffect, use("list", "assignments"), str("id")),
			tool("assignment_update", "assignments.update", "Replace an assignment's plan: pass plan (JSON) and expected_revision from get/list. Grants may only narrow — widening needs the owner's chat-side authorize, which no tool can perform.", api.WriteEffect, use("update", "assignments"), str("id"), str("plan"), arg("expected_revision", "integer", true)),
			tool("assignment_apply", "assignments.update", "Provision or repair an assignment's schedule artifacts (idempotent). Requires expected_revision.", api.WriteEffect, use("update", "assignments"), str("id"), arg("expected_revision", "integer", true)),
			tool("assignment_pause", "assignments.update", "Pause an assignment and all its sources; wakes and deliveries stop until resume. Requires expected_revision.", api.WriteEffect, use("update", "assignments"), str("id"), arg("expected_revision", "integer", true)),
			tool("assignment_resume", "assignments.update", "Resume a paused assignment; schedules recompute their next due times. Requires expected_revision.", api.WriteEffect, use("update", "assignments"), str("id"), arg("expected_revision", "integer", true)),
			tool("assignment_cancel", "assignments.delete", "Cancel an assignment — terminal: sources retire and stale effects stay fenced. Does not recall messages already sent. Requires expected_revision.", api.WriteEffect, use("delete", "assignments"), str("id"), arg("expected_revision", "integer", true)),
			tool("assignment_complete", "assignments.update", "Mark an assignment completed and retire its sources. Requires expected_revision.", api.WriteEffect, use("update", "assignments"), str("id"), arg("expected_revision", "integer", true)),
			tool("document_extract", "documents.read", "Extract text from a document inside the admitted scope.", api.ReadEffect, use("read", "documents", "path"), path),
			tool("document_create", "documents.create", "Create a document artifact from supplied text.", api.WriteEffect, use("create", "documents"), str("name"), str("format"), str("text")),
			tool("document_edit", "documents.edit", "Replace the text of a document artifact inside the admitted scope.", api.WriteEffect, use("edit", "documents", "path"), path, str("text")),
			tool("document_convert", "documents.convert", "Convert a document inside the admitted scope into a new artifact.", api.WriteEffect, append(use("convert", "documents", "path"), use("create", "artifacts")...), path, str("name"), str("format")),
			tool("image_inspect", "images.read", "Inspect one or more images inside the admitted scope.", api.ReadEffect, use("read", "images", "path"), path),
			tool("images_inspect", "images.read", "Compare up to eight images in one request within an admitted directory.", api.ReadEffect, use("read", "images", "paths"), arg("paths", "array", true)),
			tool("archive_create", "archives.create", "Pack workspace files into a new zip under artifacts.", api.WriteEffect, append(use("read", "files", "paths"), use("create", "artifacts", "path")...), path, arg("paths", "array", true)),
			tool("archive_unpack", "archives.unpack", "Unpack a zip inside the admitted scope into artifacts/unpacked.", api.WriteEffect, append(use("read", "files", "path"), use("create", "artifacts")...), path, str("name")),
			tool("inbox_fetch", "inbox.fetch", "Materialize a file the user sent to the chat into workspace inbox/; a zip is unpacked from memory and never written as an archive.", api.WriteEffect, append(use("fetch", "inbound"), use("create", "files")...), str("id")),
			tool("knowledge_list", "knowledge.list", "List read-only knowledge sources staged for this user on the hub.", api.ReadEffect, use("read", "knowledge")),
			tool("knowledge_search", "knowledge.search", "Search read-only knowledge; excerpts include source, line and content hash.", api.ReadEffect, use("read", "knowledge"), str("query")),
			tool("knowledge_read", "knowledge.read", "Read one staged knowledge source; the content returns as tool output and never lands in the workspace.", api.ReadEffect, use("read", "knowledge"), str("name")),
			tool("image_convert", "images.convert", "Convert an image inside the admitted scope into a new artifact.", api.WriteEffect, append(use("convert", "images", "path"), use("create", "artifacts")...), str("name"), path, str("format")),
			tool("artifact_remove", "artifacts.delete", "Remove one artifact inside the admitted scope.", api.WriteEffect, use("delete", "artifacts", "path"), path),
			tool("image_generate", "image_gen.generate", "Generate an image with the granted provider.", api.WriteEffect, use("generate", "images"), str("name"), str("prompt")),
			tool("image_edit", "image_gen.edit", "Edit an image inside the admitted scope with the granted provider.", api.WriteEffect, use("edit", "images", "path"), str("name"), path, str("prompt")),
			{
				Name:         "code_exec",
				CapabilityID: "terminal.exec",
				Effect:       api.WriteEffect,
				Sandboxed:    true,
				Description:  "Run a bounded script in a disposable container: read-only inputs from the admitted scope, private scratch, no network; exported files land under the scope. Optional `skill` leases that skill's stored credentials into this one call — nothing else ever sees them.",
				Uses:         append(append(use("exec", "terminal"), use("read", "files", "path")...), use("write", "files", "path")...),
				Arguments: []api.CLIArgument{
					arg("command", "string", true), arg("path", "string", true), opt("skill"),
				},
			},
			tool("hh_search", "hh.search", "Search HeadHunter vacancies for the authorized query.", api.ReadEffect, use("read", "hh"), str("query"), api.CLIArgument{Name: "page", Type: "integer"}),
			tool("hh_vacancy", "hh.vacancy", "Read one HeadHunter vacancy.", api.ReadEffect, use("read", "hh"), str("id")),
			tool("hh_resumes", "hh.resumes", "List the owner resumes on HeadHunter.", api.ReadEffect, use("read", "hh")),
			tool("hh_apply", "hh.apply", "Apply to one HeadHunter vacancy with the reviewed resume and message.", api.WriteEffect, use("apply", "hh"), str("id"), str("resume_id"), str("message"), api.CLIArgument{Name: "authorized", Type: "boolean", Required: true}),
			tool("ssh_hosts", "ssh.list", "List SSH hosts declared in the reviewed capability config.", api.ReadEffect, use("list", "ssh")),
			tool("ssh_exec", "ssh.exec", "Run a bounded command on a declared SSH host.", api.WriteEffect, use("exec", "ssh"), str("host"), str("command")),
			tool("ssh_read", "ssh.read", "Read a remote file on a declared SSH host.", api.ReadEffect, use("read", "ssh"), str("host"), path),
			tool("ssh_write", "ssh.write", "Write a remote file on a declared SSH host when the write grant is active.", api.WriteEffect, use("write", "ssh"), str("host"), path, str("text")),
			tool("ssh_shell_open", "ssh.shell", "Open an interactive shell on a declared SSH host when the shell grant is active.", api.WriteEffect, use("shell", "ssh"), str("host")),
			tool("ssh_shell_send", "ssh.shell_send", "Send input to an open SSH shell.", api.WriteEffect, use("shell", "ssh"), str("id"), str("data")),
			tool("ssh_shell_read", "ssh.shell_read", "Read output from an open SSH shell.", api.ReadEffect, use("shell", "ssh"), str("id"), api.CLIArgument{Name: "offset", Type: "integer"}),
			tool("ssh_shell_close", "ssh.shell_close", "Close an open SSH shell.", api.WriteEffect, use("shell", "ssh"), str("id")),
			tool("ssh_tunnel_open", "ssh.tunnel", "Open a tunnel through a declared SSH host when the tunnel grant is active.", api.WriteEffect, use("tunnel", "ssh"), str("host"), str("name")),
			tool("ssh_tunnel_list", "ssh.tunnel_list", "List open SSH tunnels.", api.ReadEffect, use("tunnel", "ssh")),
			tool("ssh_tunnel_close", "ssh.tunnel_close", "Close an SSH tunnel.", api.WriteEffect, use("tunnel", "ssh"), str("id")),
			tool("site_list", "hosting.list", "List own hosted sites without share tokens.", api.ReadEffect, use("list", "hosting")),
			tool("site_status", "hosting.status", "Show own site state and revision.", api.ReadEffect, use("read", "hosting"), str("name")),
			tool("site_unpublish", "hosting.unpublish", "Revoke and remove an own site revision.", api.WriteEffect, use("remove", "hosting"), str("name"), str("request_id"), arg("expected_revision", "integer", true)),
			tool("site_publish", "hosting.publish", "Publish prebuilt workspace files to an own site slot.", api.WriteEffect, append(use("publish", "hosting"), use("read", "files", "path")...), str("name"), path, str("request_id"), arg("expected_revision", "integer", true), opt("mode"), opt("access"), opt("profile"), arg("lifetime_seconds", "integer", false), arg("authorized", "boolean", false)),
			tool("exposure_list", "exposure.list", "List live public exposures on this runtime.", api.ReadEffect, use("list", "exposure")),
			tool("exposure_status", "exposure.status", "Show one live public exposure by endpoint name.", api.ReadEffect, use("read", "exposure"), str("name")),
			tool("exposure_open", "exposure.open", "Open one declared endpoint's public tunnel.", api.WriteEffect, use("open", "exposure"), str("name")),
			tool("exposure_close", "exposure.close", "Close one live public exposure by endpoint name.", api.WriteEffect, use("close", "exposure"), str("name")),
			// SPEC-0083 R5.*: governed skills surface. Reads live under the
			// skills family, every mutation under skills_manage and always
			// lands as a draft — publication is a human confirmation.
			tool("skill", "skills.invoke", "Activate a skill by name: returns its frontmatter-stripped instructions, resource file names (never contents) and the read-only skill directory.", api.ReadEffect, use("invoke", "skills"), str("name"), opt("args")),
			tool("skills_search", "skills.read", "Search the eligible skill catalog by name and description; returns name, source and short description.", api.ReadEffect, use("read", "skills"), str("query"), api.CLIArgument{Name: "limit", Type: "integer"}, opt("source")),
			tool("skills_list", "skills.read", "List eligible skills in pages of 20 with source, description and usage counters.", api.ReadEffect, use("read", "skills"), opt("source"), api.CLIArgument{Name: "offset", Type: "integer"}),
			tool("skill_view", "skills.read", "Inspect one skill: file listing or bounded file text; version reads a retained revision.", api.ReadEffect, use("read", "skills"), str("name"), opt("path"), opt("version")),
			tool("skill_manage", "skills_manage.manage", "Propose a skill change: create/patch/edit/write_file/remove_file/delete/archive/restore/propose_to_org, install/update from a GitHub or GitLab URL, check_updates. Every mutating action returns a draft with diff, lint and scan — nothing goes live without the owner's approval card.", api.WriteEffect, use("manage", "skills_manage"),
				str("action"), str("name"), str("reason"), opt("content"), opt("category"), opt("old_string"), opt("new_string"), opt("file_path"), opt("base_revision"), opt("absorbed_into"), opt("version"), opt("url"), opt("ref"), opt("path"), opt("grant")),
		},
	}
}
