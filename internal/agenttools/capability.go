package agenttools

import (
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// HubToolsDefinitionID is the single in-process tool catalog the managed
// ToolHub projects for a runtime. There is no remote source: execution is the
// private per-call executor (hubctl tools-exec) inside the owning runtime.
const HubToolsDefinitionID = "hub-agent-tools"

// HubToolsDefinition returns the reviewed agent-tools catalog: one ToolSpec
// per callable under the catalog r5 vocabulary (ADR-0094 D1). Every model
// tool carries its family capability id, its RBAC `requires` and the
// executor that runs the call behind the PEP — `handler:<family>` for the
// in-runtime agenttools executor, `runtime:<api>`/`connector:<name>`/
// `flow:<name>` for surfaces dispatched outside it. `internal`/`operator`
// tools execute through ToolHub but are never placeable or model-visible.
func HubToolsDefinition() api.ToolDefinition {
	arg := func(name, typ string, required bool) api.CLIArgument {
		return api.CLIArgument{Name: name, Type: typ, Required: required}
	}
	path := arg("path", "string", true)
	str := func(name string) api.CLIArgument { return arg(name, "string", true) }
	opt := func(name string) api.CLIArgument { return arg(name, "string", false) }
	intArg := func(name string, required bool) api.CLIArgument { return arg(name, "integer", required) }
	boolArg := func(name string, required bool) api.CLIArgument { return arg(name, "boolean", required) }
	arrArg := func(name string, required bool) api.CLIArgument { return arg(name, "array", required) }
	objArg := func(name string, required bool) api.CLIArgument { return arg(name, "object", required) }
	use := func(action, resource string, args ...string) []api.CapabilityUse {
		if len(args) > 0 {
			return []api.CapabilityUse{{Action: action, Resource: resource, PathArgument: args[0]}}
		}
		return []api.CapabilityUse{{Action: action, Resource: resource}}
	}
	// tool builds a model-audience spec whose executor defaults to the
	// in-runtime handler of its capability family.
	tool := func(name, capability, right, description string, effect api.Effect, uses []api.CapabilityUse, arguments ...api.CLIArgument) api.ToolSpec {
		family, _, _ := strings.Cut(capability, ".")
		return api.ToolSpec{
			Name: name, Effect: effect, CapabilityID: capability,
			Requires: []string{right}, Description: description, Uses: uses,
			Arguments: arguments, Audience: api.AudienceModel, Executor: "handler:" + family,
		}
	}
	// remote builds a spec executed outside the in-runtime handler —
	// connector:, flow: or runtime: executors; still cataloged by the one
	// definition so projection and rights stay single-sourced.
	remote := func(name, capability, right, description string, effect api.Effect, executor api.Executor, arguments ...api.CLIArgument) api.ToolSpec {
		return api.ToolSpec{
			Name: name, Effect: effect, CapabilityID: capability,
			Requires: []string{right}, Description: description,
			Arguments: arguments, Audience: api.AudienceModel, Executor: executor,
		}
	}
	def := api.ToolDefinition{
		Schema:       api.SchemaVersion,
		DefinitionID: HubToolsDefinitionID,
		Version:      "8.0.0",
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
			Egress:         []string{"api.hh.ru", "communication-hub"},
		},
		Health: api.HealthProbe{Kind: "exec", Value: "hubctl", TimeoutSeconds: 5},
		Tools: []api.ToolSpec{
			// ---- files (8): absorbs archives, artifacts, knowledge; inbox is
			// internal. root selects the admitted filesystem root.
			tool("file_list", "files.list", "workspace.read", "List files under an admitted root.", api.ReadEffect, use("list", "files", "path"), opt("root"), opt("path"), intArg("limit", false), opt("cursor")),
			tool("file_read", "files.read", "workspace.read", "Read one file under an admitted root with bounded output; pass next_offset as offset for the following chunk.", api.ReadEffect, use("read", "files", "path"), opt("root"), path, intArg("offset", false), intArg("limit", false)),
			tool("file_search", "files.search", "workspace.read", "Search file contents under an admitted root; returns matching snippets with line numbers.", api.ReadEffect, use("search", "files", "path"), opt("root"), opt("path"), str("query"), intArg("limit", false)),
			tool("file_write", "files.write", "workspace.write", "Create or overwrite one file under a writable root, creating parent directories; revision-guarded on existing files.", api.WriteEffect, use("write", "files", "path"), opt("root"), path, str("text"), opt("revision")),
			tool("file_edit", "files.edit", "workspace.write", "Replace text inside one existing file under a writable root; revision-guarded.", api.WriteEffect, use("edit", "files", "path"), opt("root"), path, str("old_string"), str("new_string"), boolArg("replace_all", false), opt("revision")),
			tool("file_mkdir", "files.mkdir", "workspace.write", "Create a directory under a writable root.", api.WriteEffect, use("create", "files", "path"), opt("root"), path, boolArg("parents", false)),
			tool("file_archive", "files.archive", "workspace.write", "Pack files into an archive or unpack one inside the admitted scope.", api.WriteEffect, append(use("read", "files", "paths"), use("create", "artifacts", "path")...), str("action"), opt("path"), arrArg("paths", false), opt("name")),
			tool("file_remove", "files.remove", "workspace.write", "Delete one file under a writable root.", api.WriteEffect, use("remove", "files", "path"), opt("root"), path),
			// Project creation is a distinct right: a new top-level directory
			// under projects/ never appears through file_write — the model
			// calls this once and plain writes land inside the project.
			tool("project_create", "files.create_project", "workspace.create_project", "Initialize a new project directory under projects/; file_write then works inside it. Existing names return created=false.", api.WriteEffect, use("create", "projects"), str("name")),
			{
				Name: "inbox_fetch", CapabilityID: "inbox.fetch", Requires: []string{"workspace.read"},
				Effect: api.WriteEffect, Audience: api.AudienceInternal, Executor: "handler:files",
				Description: "Materialize a file the user sent to the chat into workspace inbox/; a zip is unpacked from memory and never written as an archive.",
				Uses:        append(use("fetch", "inbound"), use("create", "files")...), Arguments: []api.CLIArgument{str("id")},
			},

			// ---- documents (5)
			tool("document_extract", "documents.read", "workspace.read", "Extract text from a document inside the admitted scope. Supported: txt, md, csv, html, pdf, xlsx, pptx; doc and docx stay unsupported.", api.ReadEffect, use("read", "documents", "path"), path),
			tool("document_create", "documents.create", "workspace.write", "Create a document artifact from supplied text.", api.WriteEffect, use("create", "documents"), str("name"), str("format"), str("text")),
			tool("document_edit", "documents.edit", "workspace.write", "Replace the text of a document artifact inside the admitted scope.", api.WriteEffect, use("edit", "documents", "path"), path, str("text")),
			tool("document_convert", "documents.convert", "workspace.write", "Convert a document inside the admitted scope into a new artifact.", api.WriteEffect, append(use("convert", "documents", "path"), use("create", "artifacts")...), path, str("name"), str("format")),
			tool("document_remove", "documents.remove", "workspace.write", "Delete one document artifact inside the admitted scope.", api.WriteEffect, use("remove", "documents", "path"), path),

			// ---- media (7): images, conversion, generation, STT/TTS
			tool("media_inspect", "media.inspect", "workspace.read", "Inspect or compare images inside the admitted scope.", api.ReadEffect, use("read", "media", "paths"), arrArg("paths", true)),
			tool("media_convert", "media.convert", "workspace.write", "Convert a media file inside the admitted scope into a new artifact.", api.WriteEffect, append(use("convert", "media", "path"), use("create", "artifacts")...), str("name"), path, str("format")),
			tool("media_generate", "media.generate", "workspace.write", "Generate an image or submit a video job with the granted provider.", api.WriteEffect, use("generate", "media"), str("name"), str("prompt"), str("kind")),
			tool("media_edit", "media.edit", "workspace.write", "Edit an image inside the admitted scope with the granted provider.", api.WriteEffect, use("edit", "media", "path"), str("name"), path, str("prompt")),
			tool("media_fetch", "media.fetch", "workspace.read", "Check one media job by id and stage the finished result under artifacts.", api.ReadEffect, use("fetch", "media"), str("id"), opt("name")),
			tool("media_transcribe", "media.transcribe", "workspace.read", "Transcribe an audio file inside the admitted scope to text.", api.ReadEffect, use("read", "media", "path"), path, opt("language"), boolArg("timestamps", false)),
			tool("media_speak", "media.speak", "channels.send_voice", "Synthesize text to audio delivered through the active channel.", api.WriteEffect, use("speak", "media"), str("text"), opt("language")),

			// ---- terminal (2): persistent exec + job control. Runs on the
			// in-runtime daemon executor — background jobs live in its
			// registry and outlive the call (#441 S1).
			{
				Name: "terminal_exec", CapabilityID: "terminal.exec", Requires: []string{"exec.run"},
				Effect: api.WriteEffect, Audience: api.AudienceModel, Executor: "handler:terminal",
				Description: "Run a bounded command in the workspace execution environment; returns bounded output or a job id for background runs.",
				Uses:        append(append(use("exec", "terminal"), use("read", "files", "path")...), use("write", "files", "path")...),
				Arguments: []api.CLIArgument{
					str("cmd"), opt("cwd"), intArg("timeout", false), opt("stdin"), opt("shell"),
					boolArg("background", false), opt("path"), opt("skill"),
				},
			},
			tool("terminal_job", "terminal.job", "exec.run", "Poll, feed, kill or list background jobs started by terminal_exec.", api.WriteEffect, use("exec", "jobs"), str("action"), opt("job"), opt("data")),

			// ---- ssh (6): consolidated from 11
			tool("ssh_list", "ssh.list", "ssh.list", "List SSH hosts declared in the reviewed capability config.", api.ReadEffect, use("list", "ssh")),
			tool("ssh_read", "ssh.read", "ssh.read", "Read a remote file on a declared SSH host.", api.ReadEffect, use("read", "ssh"), str("host"), path),
			tool("ssh_write", "ssh.write", "ssh.write", "Write a remote file on a declared SSH host when the write grant is active.", api.WriteEffect, use("write", "ssh"), str("host"), path, str("text")),
			tool("ssh_exec", "ssh.exec", "ssh.exec", "Run a bounded command on a declared SSH host.", api.WriteEffect, use("exec", "ssh"), str("host"), str("command"), intArg("timeout", false)),
			tool("ssh_shell", "ssh.shell", "ssh.shell", "Drive an interactive shell on a declared SSH host: open a session, send input, read output, close.", api.WriteEffect, use("shell", "ssh"), str("action"), opt("host"), opt("session"), opt("data"), intArg("offset", false)),
			tool("ssh_tunnel", "ssh.tunnel", "ssh.tunnel", "Open, list or close tunnels through a declared SSH host.", api.WriteEffect, use("tunnel", "ssh"), str("action"), opt("host"), opt("name"), opt("session")),

			// ---- exposure (3): consolidated from 4
			tool("exposure_list", "exposure.list", "exposure.list", "List live public exposures or show one by id, including uncertain effects requiring reconciliation.", api.ReadEffect, use("list", "exposure"), opt("id")),
			tool("exposure_create", "exposure.create", "exposure.create", "Open one declared endpoint's public tunnel.", api.WriteEffect, use("create", "exposure"), str("name")),
			tool("exposure_remove", "exposure.remove", "exposure.remove", "Close one live public exposure; a close still attempts to stop access on audit failure.", api.WriteEffect, use("remove", "exposure"), str("name")),

			// ---- routines (5)
			tool("routine_list", "routines.list", "tasks.schedule", routineDescriptions["routine_list"], api.ReadEffect, use("list", "routines"), intArg("limit", false), opt("cursor"), opt("kind"), opt("state"), opt("query"), opt("view")),
			tool("routine_read", "routines.read", "tasks.schedule", routineDescriptions["routine_get"], api.ReadEffect, use("read", "routines"), str("id"), arrArg("include", false), intArg("history_limit", false), opt("history_cursor"), intArg("preview_limit", false)),
			tool("routine_create", "routines.create", "tasks.schedule", routineDescriptions["routine_create"], api.WriteEffect, use("create", "routines"), str("id"), str("timezone"), str("expression"), str("text"), opt("origin_job_id"), opt("destination_task_id"), opt("kind"), opt("url"), opt("regex"), opt("expect"), opt("probe_kind"), opt("tool"), objArg("args", false), opt("service"), arrArg("steps", false), opt("wait_for"), arrArg("needs", false), opt("recipe"), arrArg("extract", false), opt("probe_script"), opt("deliver_mode"), opt("artifact_name"), opt("notify_mode"), opt("quiet_from"), opt("quiet_to"), intArg("daily_limit", false), intArg("max_retries", false), arrArg("source_ids", false), objArg("timing", false)),
			tool("routine_edit", "routines.edit", "tasks.schedule", routineDescriptions["routine_edit"], api.WriteEffect, use("update", "routines"), str("id"), str("action"), intArg("expected_revision", true), objArg("patch", false), opt("until"), opt("origin_job_id")),
			tool("routine_remove", "routines.remove", "tasks.schedule", routineDescriptions["routine_delete"], api.WriteEffect, use("remove", "routines"), str("id"), intArg("expected_revision", true)),

			// ---- watches (5)
			tool("watch_list", "watches.list", "tasks.observe", routineDescriptions["watch_list"], api.ReadEffect, use("list", "watches")),
			tool("watch_create", "watches.create", "tasks.observe", routineDescriptions["watch_subscribe"], api.WriteEffect, use("create", "watches"), str("id"), str("timezone"), str("expression"), str("text"), str("tool"), opt("sink"), objArg("args", false), opt("delivery"), opt("filter"), arrArg("sources", false), opt("cursor_arg"), intArg("min_interval_seconds", false)),
			tool("watch_edit", "watches.edit", "tasks.observe", "Pause or resume one owned event subscription by schedule id.", api.WriteEffect, use("update", "watches"), str("id"), str("action")),
			tool("watch_remove", "watches.remove", "tasks.observe", routineDescriptions["watch_delete"], api.WriteEffect, use("remove", "watches"), str("id")),
			tool("watch_sample", "watches.sample", "tasks.observe", routineDescriptions["watch_sample"], api.ReadEffect, use("read", "watches"), str("id"), opt("filter")),

			// ---- assignments (6): plan stays separate, lifecycle via edit
			tool("assignment_list", "assignments.list", "tasks.delegate", "List the owner's standing assignments: id, title, state, blocked reason, durable counters.", api.ReadEffect, use("list", "assignments"), intArg("limit", false), opt("cursor")),
			tool("assignment_read", "assignments.read", "tasks.delegate", "Read one assignment card: state, applied schedule receipts, plan revision, grants, checkpoint counters, blocked reason.", api.ReadEffect, use("read", "assignments"), str("id")),
			tool("assignment_plan", "assignments.plan", "tasks.delegate", "Dry-run a saved draft assignment plan before apply: returns per-source structural verdicts; persists nothing.", api.ReadEffect, use("plan", "assignments"), objArg("plan", true)),
			tool("assignment_create", "assignments.create", "tasks.delegate", "Save the owner's verbatim intent as a durable draft before checking any sources; grants bound LLM spend.", api.WriteEffect, use("create", "assignments"), str("text"), opt("title"), objArg("plan", false), boolArg("queue", false)),
			tool("assignment_edit", "assignments.edit", "tasks.delegate", "Steer one assignment: update plan, apply, pause, resume, record progress, cancel or complete. Cancel terminates work; removal is assignment_remove.", api.WriteEffect, use("update", "assignments"), str("id"), str("action"), intArg("expected_revision", true), objArg("plan", false), opt("job_id"), opt("summary"), opt("finding"), opt("next_step"), boolArg("complete", false)),
			tool("assignment_remove", "assignments.remove", "tasks.delegate", "Delete one assignment record; does not recall messages already sent.", api.WriteEffect, use("remove", "assignments"), str("id"), intArg("expected_revision", true)),

			// ---- goals (3)
			tool("goal_status", "goals.read", "goals.read", "Show this conversation's goal record: status, turns used against the budget, last verdict and bounded reason.", api.ReadEffect, use("read", "goals")),
			tool("goal_edit", "goals.edit", "goals.edit", "Set, pause or resume this conversation's persistent goal.", api.WriteEffect, use("update", "goals"), str("action"), opt("text")),
			tool("goal_remove", "goals.remove", "goals.remove", "Clear this conversation's goal record entirely.", api.WriteEffect, use("remove", "goals")),

			// ---- context (2)
			tool("context_status", "context.read", "context.read", "Show this session's context control state: effective threshold, journaled compaction count and last event, live token estimate.", api.ReadEffect, use("read", "context"), opt("id"), opt("context")),
			tool("context_edit", "context.edit", "context.edit", "Compact this session's transcript at a turn boundary (action=compact): prune tool outputs, summarize the head, keep a verbatim tail.", api.WriteEffect, use("update", "context"), str("action"), opt("id"), opt("context"), opt("focus"), opt("engine"), intArg("here", false), boolArg("prune", false), boolArg("preview", false), boolArg("queue", false)),

			// ---- settings (2)
			tool("settings_read", "settings.read", "config.read", "Show runtime self-setting overrides: the manageable key allowlist, current overrides and their effective values. Secret values are never shown.", api.ReadEffect, use("read", "settings"), opt("key")),
			tool("settings_edit", "settings.edit", "config.edit_own", "Set one manageable runtime setting or reset it to the rendered default. Applies after the scheduled runtime restart.", api.WriteEffect, use("update", "settings"), str("key"), opt("value")),

			// ---- sessions (6): hub-runtime SessionStore; unavailable without it
			remote("session_list", "sessions.list", "sessions.list", "List sessions of this space: id, channel, created/last_active, status, size.", api.ReadEffect, "runtime:sessions", intArg("limit", false), opt("cursor")),
			remote("session_search", "sessions.search", "sessions.search", "Search sessions of this space by content or title.", api.ReadEffect, "runtime:sessions", str("query"), intArg("limit", false)),
			remote("session_read", "sessions.read", "sessions.read", "Read a bounded transcript of one session in this space.", api.ReadEffect, "runtime:sessions", str("id"), intArg("offset", false), intArg("limit", false)),
			remote("session_switch", "sessions.switch", "sessions.switch", "Move the active-session pointer; returns switched_to and resumed_from.", api.WriteEffect, "runtime:sessions", str("id")),
			remote("session_edit", "sessions.edit", "sessions.manage", "Archive or restore one session of this space; archive keeps the transcript.", api.WriteEffect, "runtime:sessions", str("id"), str("action")),
			remote("session_remove", "sessions.remove", "sessions.manage", "Delete one session of this space terminally.", api.WriteEffect, "runtime:sessions", str("id")),

			// ---- models (2): hub-side model-relay serves the passport view
			remote("model_list", "models.list", "models.list", "List routes, ladders and per-service status from the models passport.", api.ReadEffect, "runtime:models"),
			remote("model_switch", "models.switch", "models.switch", "Switch the active model or route inside the granted ladder.", api.WriteEffect, "runtime:models", str("name")),

			// ---- skills (3): Agent Skills invocation convention
			tool("skill", "skills.invoke", "skills.invoke", "Activate a skill by name: returns its frontmatter-stripped instructions, resource file names (never contents) and the read-only skill directory.", api.ReadEffect, use("invoke", "skills"), str("name"), opt("path")),
			tool("skill_search", "skills.search", "skills.list", "Search or list the skill catalog: name, source, description, eligibility and usage counters; an empty query lists all.", api.ReadEffect, use("read", "skills"), opt("query"), intArg("limit", false), opt("source"), intArg("offset", false)),
			tool("skill_edit", "skills.edit", "skills.draft", "Propose a skill change: create/patch/edit/write_file/remove_file/delete/archive/restore/propose_to_org/install/update/install_local/check_updates/configure. Every mutating action returns a draft with diff, lint and scan.", api.WriteEffect, use("manage", "skills"),
				str("action"), str("name"), opt("reason"), opt("content"), opt("category"), opt("old_string"), opt("new_string"), opt("file_path"), opt("base_revision"), opt("absorbed_into"), opt("version"), opt("url"), opt("ref"), opt("path"), opt("grant")),

			// ---- connectors (5): ToolHub connector control; credentials never
			// travel through tool args or results.
			remote("connector_search", "connectors.search", "connectors.discover", "Search the connector catalog by name and kind; returns connector ids and availability.", api.ReadEffect, "runtime:connectors", opt("query"), opt("kind"), intArg("limit", false)),
			remote("connector_list", "connectors.list", "connectors.discover", "List connectors visible to this space with their status.", api.ReadEffect, "runtime:connectors"),
			remote("connector_status", "connectors.status", "connectors.discover", "Show one connector's status; verbose adds diagnostics.", api.ReadEffect, "runtime:connectors", str("name"), boolArg("verbose", false)),
			remote("connector_install", "connectors.install", "connectors.prepare", "Install a connector candidate from connector_search; returns a protected credential form when required — never accepts secrets in arguments.", api.WriteEffect, "runtime:connectors", str("name"), opt("version")),
			remote("connector_edit", "connectors.edit", "connectors.enable", "Change a connector's lifecycle: enable, disable, allow_writes, reauth (returns a protected URL only) or remove.", api.WriteEffect, "runtime:connectors", str("name"), str("action")),

			// ---- web (2): hub-side web provider relay; cataloged names over
			// the space's configured search/fetch backends
			remote("web_search", "web.search", "web.search", "Search the web through the space's configured provider.", api.ReadEffect, "runtime:web", str("query"), intArg("limit", false)),
			remote("web_fetch", "web.fetch", "web.read", "Fetch one URL's content as bounded text; untrusted data.", api.ReadEffect, "runtime:web", str("url")),

			// ---- sites (3): publishing lifecycle; was hosting
			tool("site_list", "sites.list", "stack.inspect", "List own hosted sites or show one by id, without share tokens.", api.ReadEffect, use("list", "sites"), opt("id")),
			tool("site_publish", "sites.publish", "stack.change", "Publish prebuilt workspace files to an own site slot.", api.WriteEffect, append(use("publish", "sites"), use("read", "files", "path")...), str("name"), path, str("request_id"), intArg("expected_revision", true), opt("mode"), opt("access"), opt("profile"), intArg("lifetime_seconds", false), boolArg("authorized", false)),
			tool("site_remove", "sites.remove", "stack.change", "Revoke and remove an own site revision terminally.", api.WriteEffect, use("remove", "sites"), str("name"), str("request_id"), intArg("expected_revision", true)),

			// ---- research (3): async deep-research flow
			remote("research_create", "research.create", "research.create", "Start an async deep-research job on a topic; returns a job id.", api.WriteEffect, "flow:deep_research", str("topic"), opt("depth"), arrArg("sources", false)),
			remote("research_read", "research.read", "research.read", "Read a research job's status and report reference.", api.ReadEffect, "flow:deep_research", str("id")),
			remote("research_list", "research.list", "research.read", "List research jobs of this space.", api.ReadEffect, "flow:deep_research", intArg("limit", false)),

			// ---- browser (4): curated names over the browser connector
			remote("browser_open", "browser.open", "browser.open", "Open a browser session on a URL under the user or guest profile; returns a session id.", api.WriteEffect, "connector:browser", str("url"), opt("profile")),
			remote("browser_read", "browser.read", "browser.read", "Read a browser session's page content or snapshot as bounded markdown.", api.ReadEffect, "connector:browser", str("session")),
			remote("browser_act", "browser.act", "browser.act", "Act in a browser session: click, type, scroll, press, select, wait or close.", api.WriteEffect, "connector:browser", str("session"), str("action"), opt("target"), opt("value")),
			remote("browser_capture", "browser.capture", "browser.capture", "Capture a browser session as a screenshot or pdf written to the workspace.", api.ReadEffect, "connector:browser", str("session"), str("kind")),

			// ---- platform (6): pinned discovery/self layer
			remote("platform_guide", "platform.guide", "platform.guide", "Orient in this space: channels, active families, working directory and how to reach the rest.", api.ReadEffect, "runtime:toolhub"),
			remote("platform_status", "platform.status", "platform.status", "Show own health and diagnostics with allowlisted fields only.", api.ReadEffect, "runtime:toolhub"),
			remote("platform_bootstrap", "platform.bootstrap", "platform.bootstrap", "Show or complete pending first-run setup steps; idempotent after completion.", api.WriteEffect, "runtime:toolhub", opt("action"), opt("step")),
			remote("platform_usage", "platform.usage", "platform.usage", "Show space consumption: tokens and cost per model and period against models.yaml limits.", api.ReadEffect, "runtime:toolhub", opt("period")),
			remote("tool_search", "platform.search", "tools.list", "Search the extended tool inventory by name, description, parameters or definition; names matching here are callable through tool_call.", api.ReadEffect, "runtime:toolhub", opt("query"), opt("name"), intArg("limit", false)),
			remote("tool_call", "platform.call", "tools.select", "Invoke one extended tool by name with arguments; admission identical to a direct call.", api.WriteEffect, "runtime:toolhub", str("tool"), objArg("arguments", true)),
		},
	}
	applyArgDocs(&def)
	return def
}
