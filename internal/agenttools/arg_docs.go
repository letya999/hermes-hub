package agenttools

import "github.com/letya999/hermes-hub/internal/toolhub/api"

// argDocs carries the model-facing contract for every declared argument
// (tool-design §4.3): a one-line description plus, where the value is a
// closed vocabulary, the exact enum the executor validates. The schema lint
// refuses a model-facing argument missing from this table.
type argDoc struct {
	Desc  string
	Enum  []string
	Items string
}

var argDocs = map[string]map[string]argDoc{
	// ---- files
	"file_list": {
		"root":   {Desc: "Admitted filesystem root to list.", Enum: []string{"workspace", "archive", "organization", "skills", "knowledge"}},
		"path":   {Desc: "Directory path relative to the root; empty lists the root."},
		"limit":  {Desc: "Maximum entries to return."},
		"cursor": {Desc: "Pagination cursor from a previous response."},
	},
	"file_read": {
		"root":   {Desc: "Admitted filesystem root.", Enum: []string{"workspace", "archive", "organization", "skills", "knowledge"}},
		"path":   {Desc: "File path relative to the root."},
		"offset": {Desc: "Byte offset of the next chunk; pass the returned next_offset."},
		"limit":  {Desc: "Maximum bytes to return."},
	},
	"file_search": {
		"root":  {Desc: "Admitted filesystem root to search.", Enum: []string{"workspace", "archive", "organization", "skills", "knowledge"}},
		"path":  {Desc: "Directory to search under; empty searches the whole root."},
		"query": {Desc: "Text to search for."},
		"limit": {Desc: "Maximum matching snippets to return."},
	},
	"file_write": {
		"root":     {Desc: "Writable root; defaults to workspace.", Enum: []string{"workspace", "archive", "organization", "skills", "knowledge"}},
		"path":     {Desc: "Destination file path relative to the root."},
		"text":     {Desc: "Full file content; overwrites any existing file."},
		"revision": {Desc: "Expected current revision when overwriting; conflict means re-read."},
	},
	"file_edit": {
		"root":        {Desc: "Writable root.", Enum: []string{"workspace", "archive", "organization", "skills", "knowledge"}},
		"path":        {Desc: "Existing file path relative to the root."},
		"old_string":  {Desc: "Exact text to replace."},
		"new_string":  {Desc: "Replacement text."},
		"replace_all": {Desc: "Replace every occurrence instead of failing on non-unique old_string."},
		"revision":    {Desc: "Expected current revision; conflict means re-read."},
	},
	"file_mkdir": {
		"root":    {Desc: "Writable root.", Enum: []string{"workspace", "archive", "organization", "skills", "knowledge"}},
		"path":    {Desc: "Directory path to create."},
		"parents": {Desc: "Create missing parent directories too."},
	},
	"file_archive": {
		"action": {Desc: "Pack paths into an archive or unpack one.", Enum: []string{"create", "unpack"}},
		"path":   {Desc: "Archive path: destination on create, source on unpack."},
		"paths":  {Desc: "Files and directories to pack (create only).", Items: "string"},
		"name":   {Desc: "Optional archive name override."},
	},
	"file_remove": {
		"root": {Desc: "Writable root.", Enum: []string{"workspace", "archive", "organization", "skills", "knowledge"}},
		"path": {Desc: "File path to delete."},
	},
	"project_create": {
		"name": {Desc: "Project directory name under projects/; a single clean path segment."},
	},
	"inbox_fetch": {
		"id": {Desc: "Inbox id from the [file ... inbox=<id>] marker."},
	},

	// ---- documents
	"document_extract": {
		"path": {Desc: "Document path; a skills:<name>/<path> prefix reads the read-only skills view."},
	},
	"document_create": {
		"name":   {Desc: "Artifact name for the created document."},
		"format": {Desc: "Document format such as md, txt or pdf."},
		"text":   {Desc: "Document content."},
	},
	"document_edit": {
		"path": {Desc: "Existing document path."},
		"text": {Desc: "Replacement document content."},
	},
	"document_convert": {
		"path":   {Desc: "Source document path."},
		"name":   {Desc: "Artifact name for the converted document."},
		"format": {Desc: "Target format."},
	},
	"document_remove": {
		"path": {Desc: "Document artifact path to delete."},
	},

	// ---- media
	"media_inspect": {
		"paths": {Desc: "One or more image paths to inspect or compare.", Items: "string"},
	},
	"media_convert": {
		"name":   {Desc: "Artifact name for the converted media."},
		"path":   {Desc: "Source media path."},
		"format": {Desc: "Target media format."},
	},
	"media_generate": {
		"name":   {Desc: "Artifact name for the generated media."},
		"prompt": {Desc: "Generation prompt."},
		"kind":   {Desc: "What to generate.", Enum: []string{"image", "video"}},
	},
	"media_edit": {
		"name":   {Desc: "Artifact name for the edited image."},
		"path":   {Desc: "Source image path."},
		"prompt": {Desc: "Edit instructions."},
	},
	"media_fetch": {
		"id":   {Desc: "Media job id returned by generate or speak."},
		"name": {Desc: "Optional artifact name for the staged result."},
	},
	"media_transcribe": {
		"path":       {Desc: "Audio file path inside the admitted scope."},
		"language":   {Desc: "Expected audio language, when known."},
		"timestamps": {Desc: "Include segment timestamps in the transcript."},
	},
	"media_speak": {
		"text":     {Desc: "Text to synthesize into a voice message."},
		"language": {Desc: "Voice language, when known."},
	},

	// ---- memory
	"memory_search": {
		"query":   {Desc: "Text matched over daily leaves, cards and summaries."},
		"limit":   {Desc: "Maximum entries to return."},
		"from":    {Desc: "Lower entry-date bound, YYYY-MM-DD."},
		"to":      {Desc: "Upper entry-date bound, YYYY-MM-DD."},
		"kinds":   {Desc: "Entry kinds to include.", Items: "string"},
		"history": {Desc: "Include superseded/forgotten entries."},
	},
	"memory_card": {
		"op":       {Desc: "Write operation.", Enum: []string{"remember", "fact", "truth", "merge", "forget"}},
		"text":     {Desc: "Note text (remember) or evidence line (fact/truth)."},
		"card":     {Desc: "Target card id or path for fact/truth/merge."},
		"name":     {Desc: "Optional note title for remember."},
		"to":       {Desc: "Destination card for merge."},
		"id":       {Desc: "Entry id to forget."},
		"evidence": {Desc: "Source reference cited by a fact."},
		"tags":     {Desc: "Optional tags on the written entry.", Items: "string"},
	},

	// ---- terminal
	"terminal_exec": {
		"cmd":        {Desc: "Command line to run in the workspace execution environment."},
		"cwd":        {Desc: "Working directory, relative to the workspace."},
		"timeout":    {Desc: "Seconds before the run is killed."},
		"stdin":      {Desc: "Data fed to the process on stdin."},
		"shell":      {Desc: "Shell override; the default is the runtime's configured shell."},
		"background": {Desc: "Run detached; returns a job id steered by terminal_job."},
		"path":       {Desc: "Optional file path context for the capability scope."},
		"skill":      {Desc: "Skill name whose stored credentials lease into this one call."},
	},
	"terminal_job": {
		"action": {Desc: "Job control verb.", Enum: []string{"poll", "send", "kill", "list"}},
		"job":    {Desc: "Job id from a background terminal_exec."},
		"data":   {Desc: "Payload for send: text fed to the job."},
	},

	// ---- ssh
	"ssh_list": {},
	"ssh_read": {
		"host": {Desc: "Declared SSH host name."},
		"path": {Desc: "Remote file path to read."},
	},
	"ssh_write": {
		"host": {Desc: "Declared SSH host name."},
		"path": {Desc: "Remote file path to write."},
		"text": {Desc: "Content to write."},
	},
	"ssh_exec": {
		"host":    {Desc: "Declared SSH host name."},
		"command": {Desc: "Command to run on the host."},
		"timeout": {Desc: "Seconds before the command is killed."},
	},
	"ssh_shell": {
		"action":  {Desc: "Session lifecycle verb.", Enum: []string{"open", "send", "read", "close"}},
		"host":    {Desc: "Declared SSH host (open)."},
		"session": {Desc: "Shell session id (send/read/close)."},
		"data":    {Desc: "Input to send (send)."},
		"offset":  {Desc: "Read offset for the next output chunk (read)."},
	},
	"ssh_tunnel": {
		"action":  {Desc: "Tunnel lifecycle verb.", Enum: []string{"open", "list", "close"}},
		"host":    {Desc: "Declared SSH host (open)."},
		"name":    {Desc: "Tunnel name (open/close)."},
		"session": {Desc: "Shell session carrying the tunnel, when needed."},
	},

	// ---- exposure
	"exposure_list": {
		"id": {Desc: "Show one exposure by id; empty lists all."},
	},
	"exposure_create": {
		"name": {Desc: "Declared endpoint name to expose."},
	},
	"exposure_remove": {
		"name": {Desc: "Live exposure name or id to close."},
	},

	// ---- routines
	"routine_list": {
		"limit":  {Desc: "Maximum entries to return."},
		"cursor": {Desc: "Pagination cursor from a previous response."},
		"kind":   {Desc: "Filter by job kind.", Enum: []string{"agent", "reminder", "digest", "watcher", "check", "script", "brief", "event"}},
		"state":  {Desc: "Filter by schedule state."},
		"query":  {Desc: "Text filter over routine names and inputs."},
		"view":   {Desc: "Alternate listing view such as destinations."},
	},
	"routine_read": {
		"id":             {Desc: "Schedule id to read."},
		"include":        {Desc: "Optional sections: history, next_runs.", Items: "string"},
		"history_limit":  {Desc: "History entries per page (1..100)."},
		"history_cursor": {Desc: "History page cursor."},
		"preview_limit":  {Desc: "Next-runs preview count (1..20)."},
	},
	"routine_create": {
		"id":                  {Desc: "Schedule id for the new routine."},
		"timezone":            {Desc: "IANA timezone for the schedule."},
		"expression":          {Desc: "after:180s, once:RFC3339, five-field cron or every:15m."},
		"text":                {Desc: "Input text or instruction the routine runs with."},
		"origin_job_id":       {Desc: "This run's routing reference for delivery into the current task."},
		"destination_task_id": {Desc: "Delivery target: task id from routine_list view=destinations, or current."},
		"kind":                {Desc: "Job kind; agent wakes the model.", Enum: []string{"agent", "reminder", "digest", "watcher", "check", "script", "brief"}},
		"url":                 {Desc: "Probe URL for watcher/check kinds."},
		"regex":               {Desc: "Capture regex applied to probe output."},
		"expect":              {Desc: "check expectation: changed, unchanged or equals:<value>."},
		"probe_kind":          {Desc: "Watcher probe mechanism.", Enum: []string{"http", "github_issue", "mcp_tool", "tool", "script", "command", "render", "service"}},
		"tool":                {Desc: "Tool name for mcp_tool probes."},
		"args":                {Desc: "Tool arguments object for mcp_tool probes."},
		"service":             {Desc: "Named service for service probes."},
		"steps":               {Desc: "Declarative extract/render steps for render probes.", Items: "object"},
		"wait_for":            {Desc: "Terminal wait condition for render probes."},
		"needs":               {Desc: "Read-tool calls the probe requires: [{tool,args}].", Items: "object"},
		"recipe":              {Desc: "Probe recipe name when using a declared recipe."},
		"extract":             {Desc: "Extraction spec applied to probe output.", Items: "object"},
		"probe_script":        {Desc: "Script body for script probes."},
		"deliver_mode":        {Desc: "How probe output reaches the channel.", Enum: []string{"notify", "content", "artifact"}},
		"artifact_name":       {Desc: "Artifact name when deliver_mode=artifact."},
		"notify_mode":         {Desc: "Notification escalation policy.", Enum: []string{"immediate", "digest", "agent"}},
		"quiet_from":          {Desc: "Quiet window start HH:MM (with quiet_to)."},
		"quiet_to":            {Desc: "Quiet window end HH:MM (with quiet_from)."},
		"daily_limit":         {Desc: "Maximum notices per day (0..32)."},
		"max_retries":         {Desc: "check retry bound (0..8)."},
		"source_ids":          {Desc: "Digest/brief source schedule ids (max 128).", Items: "string"},
		"timing":              {Desc: "Timing window object: windows, weekdays, at, start_at, end_at."},
	},
	"routine_edit": {
		"id":                {Desc: "Schedule id to modify."},
		"action":            {Desc: "Lifecycle verb; update takes patch, snooze takes until.", Enum: []string{"update", "pause", "resume", "snooze"}},
		"expected_revision": {Desc: "Revision from read/list; conflict means re-read."},
		"patch":             {Desc: "Fields to update for action=update; null clears optional fields."},
		"until":             {Desc: "RFC3339 instant for action=snooze."},
		"origin_job_id":     {Desc: "Run routing reference when moving delivery to the current task."},
	},
	"routine_remove": {
		"id":                {Desc: "Schedule id to delete."},
		"expected_revision": {Desc: "Revision from read/list; conflict means re-read."},
	},

	// ---- watches
	"watch_list": {},
	"watch_create": {
		"id":                   {Desc: "Schedule id for the new subscription."},
		"timezone":             {Desc: "IANA timezone for the subscription."},
		"expression":           {Desc: "Poll cadence: every:15m or cron for poll sources."},
		"text":                 {Desc: "Owner-visible description of what is watched."},
		"tool":                 {Desc: "Projected read tool that acquires source events."},
		"sink":                 {Desc: "Where events land: notify messages each, agent wakes once per batch.", Enum: []string{"notify", "agent"}},
		"args":                 {Desc: "Arguments object for the source tool."},
		"delivery":             {Desc: "stream for wait-style tools; poll sources need min_interval_seconds.", Enum: []string{"poll", "stream"}},
		"filter":               {Desc: "Starlark gate: input() is the event JSON; end with emit(keep|drop|urgent)."},
		"sources":              {Desc: "Fan-out sources: up to 32 {name?,tool?,args} entries.", Items: "object"},
		"cursor_arg":           {Desc: "Source tool's cursor parameter name for non-consuming reads."},
		"min_interval_seconds": {Desc: "Minimum poll interval for poll-style tools (5..3600)."},
	},
	"watch_edit": {
		"id":     {Desc: "Subscription schedule id."},
		"action": {Desc: "Pause or resume the subscription.", Enum: []string{"pause", "resume"}},
	},
	"watch_remove": {
		"id": {Desc: "Subscription schedule id to delete."},
	},
	"watch_sample": {
		"id":     {Desc: "Subscription schedule id to sample."},
		"filter": {Desc: "Candidate Starlark filter to dry-run against live events."},
	},

	// ---- assignments
	"assignment_list": {
		"limit":  {Desc: "Maximum entries to return."},
		"cursor": {Desc: "Pagination cursor from a previous response."},
	},
	"assignment_read": {
		"id": {Desc: "Assignment id."},
	},
	"assignment_plan": {
		"plan": {Desc: "Saved draft plan object to dry-run."},
	},
	"assignment_create": {
		"text":  {Desc: "Owner's verbatim intent for the standing assignment."},
		"title": {Desc: "Short owner-facing title."},
		"plan":  {Desc: "Draft plan object; sources are verified later."},
		"queue": {Desc: "Queue for apply instead of staying a draft."},
	},
	"assignment_edit": {
		"id":                {Desc: "Assignment id."},
		"action":            {Desc: "Steering verb.", Enum: []string{"update", "apply", "pause", "resume", "progress", "cancel", "complete"}},
		"expected_revision": {Desc: "Plan revision from read; conflict means re-read."},
		"plan":              {Desc: "Replacement plan object for action=update."},
		"job_id":            {Desc: "Specialist job id for action=progress."},
		"summary":           {Desc: "Progress summary for action=progress."},
		"finding":           {Desc: "Finding text for action=progress."},
		"next_step":         {Desc: "Next step for action=progress."},
		"complete":          {Desc: "Mark the job done for action=progress."},
	},
	"assignment_remove": {
		"id":                {Desc: "Assignment id."},
		"expected_revision": {Desc: "Revision from read; conflict means re-read."},
	},

	// ---- goals
	"goal_status": {},
	"goal_edit": {
		"action": {Desc: "Goal lifecycle verb.", Enum: []string{"set", "pause", "resume"}},
		"text":   {Desc: "Goal text for action=set."},
	},
	"goal_remove": {},

	// ---- context
	"context_status": {
		"id":      {Desc: "Session id; defaults to the current session."},
		"context": {Desc: "Context selector override."},
	},
	"context_edit": {
		"action":  {Desc: "Compact the transcript at a turn boundary.", Enum: []string{"compact"}},
		"id":      {Desc: "Session id; defaults to the current session."},
		"context": {Desc: "Context selector override."},
		"focus":   {Desc: "Topic focus hint for the summary."},
		"engine":  {Desc: "Compaction engine override."},
		"here":    {Desc: "Turn boundary to compact at."},
		"prune":   {Desc: "Prune tool outputs instead of summarizing."},
		"preview": {Desc: "Report what would change without applying."},
		"queue":   {Desc: "Queue the compaction instead of running inline."},
	},

	// ---- settings
	"settings_read": {
		"key": {Desc: "Show one published config setting; omit to list reviewed keys and published values."},
	},
	"settings_edit": {
		"key":           {Desc: "Reviewed config passport setting key."},
		"value":         {Desc: "New value; omit or leave empty to remove this authored leaf."},
		"base_revision": {Desc: "Published config passport revision read immediately before this edit; stale revisions are rejected."},
	},

	// ---- sessions
	"session_list": {
		"limit":  {Desc: "Maximum entries to return."},
		"cursor": {Desc: "Pagination cursor from a previous response."},
	},
	"session_search": {
		"query": {Desc: "Title or content text to match."},
		"limit": {Desc: "Maximum entries to return."},
	},
	"session_read": {
		"id":     {Desc: "Session id to read."},
		"offset": {Desc: "Transcript entry offset for the next page."},
		"limit":  {Desc: "Maximum transcript entries to return."},
	},
	"session_switch": {
		"id": {Desc: "Session id to make active."},
	},
	"session_edit": {
		"id":     {Desc: "Session id."},
		"action": {Desc: "Archive keeps the transcript; restore reactivates.", Enum: []string{"archive", "restore"}},
	},
	"session_remove": {
		"id": {Desc: "Session id to delete."},
	},

	// ---- models
	"model_list": {},
	"model_switch": {
		"name": {Desc: "Model or route name inside the granted ladder."},
	},

	// ---- skills
	"skill": {
		"name": {Desc: "Skill name to activate."},
		"path": {Desc: "Resource path inside the skill to inspect, or a retained revision."},
	},
	"skill_search": {
		"query":  {Desc: "Search text; empty lists the honest inventory."},
		"limit":  {Desc: "Maximum hits (default 5, max 20)."},
		"source": {Desc: "Restrict to one skill source.", Enum: []string{"user", "org", "platform", "import", "git", "learned", "rollback"}},
		"offset": {Desc: "Hit offset for the next page."},
	},
	"skill_edit": {
		"action":        {Desc: "Draft action; mutating actions return a draft with diff, lint and scan.", Enum: []string{"create", "patch", "edit", "write_file", "remove_file", "delete", "archive", "restore", "propose_to_org", "install", "update", "install_local", "check_updates", "configure"}},
		"name":          {Desc: "Skill name the action targets."},
		"reason":        {Desc: "Change reason recorded on the draft."},
		"content":       {Desc: "New SKILL.md content for create/patch."},
		"category":      {Desc: "Skill category metadata."},
		"old_string":    {Desc: "Exact text to replace for patch/edit."},
		"new_string":    {Desc: "Replacement text for patch/edit."},
		"file_path":     {Desc: "Path inside the skill for write_file/remove_file."},
		"base_revision": {Desc: "Current skill revision the change is based on."},
		"absorbed_into": {Desc: "Skill this one was absorbed into for archive."},
		"version":       {Desc: "Retained revision for restore/update."},
		"url":           {Desc: "HTTPS repository URL for install."},
		"ref":           {Desc: "Tag or branch to pin; resolved to a commit."},
		"path":          {Desc: "Subtree inside the repository for install."},
		"grant":         {Desc: "Broker credential name for a private repository."},
	},

	// ---- connectors
	"connector_search": {
		"query": {Desc: "Name or keyword to find in the connector catalogs."},
		"kind":  {Desc: "Restrict to one candidate status.", Enum: []string{"prepared", "blocked", "metadata-only", "requires-preflight", "registry-source", "github-source"}},
		"limit": {Desc: "Maximum candidates to return."},
	},
	"connector_list": {},
	"connector_status": {
		"name":    {Desc: "Connector name or operation id."},
		"verbose": {Desc: "Include diagnostics in the report."},
	},
	"connector_install": {
		"name":    {Desc: "Candidate id or name from connector_search."},
		"version": {Desc: "Version or digest to pin, when the candidate offers choices."},
	},
	"connector_edit": {
		"name":   {Desc: "Connector name."},
		"action": {Desc: "Lifecycle verb; reauth returns a protected URL only.", Enum: []string{"enable", "disable", "allow_writes", "reauth", "remove"}},
	},

	// ---- web
	"web_search": {
		"query": {Desc: "Search query sent to the configured provider."},
		"limit": {Desc: "Maximum results to return."},
	},
	"web_fetch": {
		"url": {Desc: "URL to fetch as bounded text."},
	},

	// ---- sites
	"site_list": {
		"id": {Desc: "Show one site by id; empty lists all."},
	},
	"site_publish": {
		"name":              {Desc: "Site slot name."},
		"path":              {Desc: "Workspace directory of prebuilt files to publish."},
		"request_id":        {Desc: "Idempotency key for the publish."},
		"expected_revision": {Desc: "Site revision; conflict means re-read."},
		"mode":              {Desc: "static serves files; dynamic runs the go-http profile.", Enum: []string{"static", "dynamic"}},
		"access":            {Desc: "Audience for the site.", Enum: []string{"private", "link", "public"}},
		"profile":           {Desc: "Dynamic runtime profile (go-http)."},
		"lifetime_seconds":  {Desc: "Expiry for link access."},
		"authorized":        {Desc: "Required confirmation that public access is intended."},
	},
	"site_remove": {
		"name":              {Desc: "Site slot name."},
		"request_id":        {Desc: "Idempotency key for the removal."},
		"expected_revision": {Desc: "Site revision; conflict means re-read."},
	},

	// ---- research
	"research_create": {
		"topic":   {Desc: "Research question or topic."},
		"depth":   {Desc: "Research depth hint for the flow."},
		"sources": {Desc: "Optional seed sources.", Items: "string"},
	},
	"research_read": {
		"id": {Desc: "Research job id."},
	},
	"research_list": {
		"limit": {Desc: "Maximum entries to return."},
	},

	// ---- browser
	"browser_open": {
		"url":     {Desc: "URL to open."},
		"profile": {Desc: "Browser profile the session runs under.", Enum: []string{"user", "guest"}},
	},
	"browser_read": {
		"session": {Desc: "Browser session id from browser_open."},
	},
	"browser_act": {
		"session": {Desc: "Browser session id."},
		"action":  {Desc: "Page interaction verb.", Enum: []string{"click", "type", "scroll", "press", "select", "wait", "close"}},
		"target":  {Desc: "Element selector or target for the action."},
		"value":   {Desc: "Value for type/select/wait actions."},
	},
	"browser_capture": {
		"session": {Desc: "Browser session id."},
		"kind":    {Desc: "Capture format written to the workspace.", Enum: []string{"screenshot", "pdf"}},
	},

	// ---- platform
	"platform_guide":  {},
	"platform_status": {},
	"platform_bootstrap": {
		"action": {Desc: "Record a finished step with step=<name>.", Enum: []string{"done"}},
		"step":   {Desc: "Bootstrap step name to mark done."},
	},
	"platform_usage": {
		"period": {Desc: "Restrict the report to one quota window.", Enum: []string{"5h", "1d", "1w"}},
	},
	"tool_search": {
		"query": {Desc: "Text matched against names, descriptions and parameters."},
		"name":  {Desc: "Exact tool name to look up."},
		"limit": {Desc: "Maximum entries to return."},
	},
	"tool_call": {
		"tool":      {Desc: "Extended tool name from tool_search."},
		"arguments": {Desc: "Arguments object for the selected tool."},
	},
}

// applyArgDocs stamps the argDocs contract onto the definition's argument
// lists. It runs inside HubToolsDefinition so a spec literal can never emit
// an argument the charter table has not described.
func applyArgDocs(def *api.ToolDefinition) {
	for i := range def.Tools {
		docs := argDocs[def.Tools[i].Name]
		for j := range def.Tools[i].Arguments {
			doc, ok := docs[def.Tools[i].Arguments[j].Name]
			if !ok {
				continue
			}
			def.Tools[i].Arguments[j].Description = doc.Desc
			def.Tools[i].Arguments[j].Enum = doc.Enum
			if doc.Items != "" {
				def.Tools[i].Arguments[j].Items = doc.Items
			}
		}
	}
}
