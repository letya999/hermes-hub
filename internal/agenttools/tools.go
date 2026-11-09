package agenttools

import (
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/exposure"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/sshcap"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Package agenttools supplies bounded files and narrow job APIs over MCP.

const Limit = 2 * 1024 * 1024

type Input struct {
	Mode               string         `json:"mode,omitempty"`
	Access             string         `json:"access,omitempty"`
	Profile            string         `json:"profile,omitempty"`
	Session            string         `json:"session,omitempty"`
	Target             string         `json:"target,omitempty"`
	RequestID          string         `json:"request_id,omitempty"`
	LifetimeSeconds    int            `json:"lifetime_seconds,omitempty"`
	OriginJobID        string         `json:"origin_job_id,omitempty"`
	DestinationTaskID  string         `json:"destination_task_id,omitempty"`
	Context            string         `json:"context,omitempty"`
	View               string         `json:"view,omitempty"`
	exportBytes        []byte         // Private create-only scratch transport; absent from MCP input.
	Path               string         `json:"path,omitempty"`
	Root               string         `json:"root,omitempty"`
	Text               string         `json:"text,omitempty"`
	Revision           string         `json:"revision,omitempty"`
	Version            string         `json:"version,omitempty"`
	Query              string         `json:"query,omitempty"`
	Providers          []string       `json:"providers,omitempty"`
	Format             string         `json:"format,omitempty"`
	Name               string         `json:"name,omitempty"`
	Prompt             string         `json:"prompt,omitempty"`
	Language           string         `json:"language,omitempty"`
	Timestamps         bool           `json:"timestamps,omitempty"`
	ID                 string         `json:"id,omitempty"`
	ResumeID           string         `json:"resume_id,omitempty"`
	Message            string         `json:"message,omitempty"`
	Authorized         bool           `json:"authorized,omitempty"`
	Service            string         `json:"service,omitempty"`
	Page               int            `json:"page,omitempty"`
	Timezone           string         `json:"timezone,omitempty"`
	Expression         string         `json:"expression,omitempty"`
	ExpectedRevision   uint64         `json:"expected_revision,omitempty"`
	Patch              map[string]any `json:"patch,omitempty"`
	Action             string         `json:"action,omitempty"`
	Until              string         `json:"until,omitempty"`
	Include            []string       `json:"include,omitempty"`
	HistoryLimit       int            `json:"history_limit,omitempty"`
	HistoryCursor      string         `json:"history_cursor,omitempty"`
	PreviewLimit       int            `json:"preview_limit,omitempty"`
	Here               int            `json:"here,omitempty"`
	Focus              string         `json:"focus,omitempty"`
	Engine             string         `json:"engine,omitempty"`
	Prune              bool           `json:"prune,omitempty"`
	Preview            bool           `json:"preview,omitempty"`
	Queue              bool           `json:"queue,omitempty"`
	Timing             map[string]any `json:"timing,omitempty"`
	Limit              int            `json:"limit,omitempty"`
	From               string         `json:"from,omitempty"`
	To                 string         `json:"to,omitempty"`
	Kinds              []string       `json:"kinds,omitempty"`
	History            bool           `json:"history,omitempty"`
	Cursor             string         `json:"cursor,omitempty"`
	State              string         `json:"state,omitempty"`
	Host               string         `json:"host,omitempty"`
	Command            string         `json:"command,omitempty"`
	Data               string         `json:"data,omitempty"`
	Offset             uint64         `json:"offset,omitempty"`
	Source             string         `json:"source,omitempty"`
	Key                string         `json:"key,omitempty"`
	Value              string         `json:"value,omitempty"`
	Paths              []string       `json:"paths,omitempty"`
	archiveSources     *os.Root
	archiveSourceNames []string
	archiveOutput      *os.Root
	archiveOutputPath  string
	Kind               string `json:"kind,omitempty"`
	URL                string `json:"url,omitempty"`
	Regex              string `json:"regex,omitempty"`
	ProbeKind          string `json:"probe_kind,omitempty"`
	Recipe             string `json:"recipe,omitempty"`
	// Extract is a list of extraction steps applied to watched output. The
	// r5 schema types it array; the legacy JSON-string form still decodes.
	Extract     any    `json:"extract,omitempty"`
	ProbeScript string `json:"probe_script,omitempty"`
	// Needs is a list of {"tool","args"} read-tool calls the hub runs
	// before a script/command probe so needs() returns their results. The
	// r5 schema types it array; the legacy JSON-string form still decodes.
	Needs        any    `json:"needs,omitempty"`
	DeliverMode  string `json:"deliver_mode,omitempty"`
	ArtifactName string `json:"artifact_name,omitempty"`
	// Steps is a list of declarative browser actions for render probes. The
	// r5 schema types it array; the legacy JSON-string form still decodes.
	// WaitFor is the terminal wait condition the render service honors.
	Steps      any      `json:"steps,omitempty"`
	WaitFor    string   `json:"wait_for,omitempty"`
	Tool       string   `json:"tool,omitempty"`
	NotifyMode string   `json:"notify_mode,omitempty"`
	SourceIDs  []string `json:"source_ids,omitempty"`
	Expect     string   `json:"expect,omitempty"`
	QuietFrom  string   `json:"quiet_from,omitempty"`
	QuietTo    string   `json:"quiet_to,omitempty"`
	DailyLimit int      `json:"daily_limit,omitempty"`
	MaxRetries int      `json:"max_retries,omitempty"`
	// Plan carries an assignment plan object. The r5 schema types it
	// object; the legacy JSON-string form still decodes. Title names
	// the assignment for the owner. Intent rides the existing Text field.
	Plan     any    `json:"plan,omitempty"`
	Title    string `json:"title,omitempty"`
	JobID    string `json:"job_id,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Finding  string `json:"finding,omitempty"`
	NextStep string `json:"next_step,omitempty"`
	Complete bool   `json:"complete,omitempty"`
	Sink     string `json:"sink,omitempty"`
	Delivery string `json:"delivery,omitempty"`
	// Args is the tool-arguments object for mcp_tool/event probes. The r5
	// schema types it object; the legacy JSON-string form still decodes.
	Args any `json:"args,omitempty"`
	// Filter is the deterministic per-event Starlark gate for event
	// subscriptions (probe.filter); MinIntervalSeconds declares the source
	// cadence for stream delivery on poll-style tools.
	Filter             string `json:"filter,omitempty"`
	MinIntervalSeconds int    `json:"min_interval_seconds,omitempty"`
	CursorArg          string `json:"cursor_arg,omitempty"`
	// Sources fans one subscription out over several acquisition inputs: a
	// list of {name?,tool?,args} entries merged onto the probe's tool+args
	// per stream round. The r5 schema types it array; the legacy
	// JSON-string form still decodes.
	Sources any `json:"sources,omitempty"`
	// Surgical file-edit and mkdir fields (files family r5).
	OldString  string `json:"old_string,omitempty"`
	NewString  string `json:"new_string,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	Parents    bool   `json:"parents,omitempty"`
	// Terminal exec fields (terminal family r5).
	Cmd        string `json:"cmd,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Stdin      string `json:"stdin,omitempty"`
	Shell      string `json:"shell,omitempty"`
	Background bool   `json:"background,omitempty"`
	Job        string `json:"job,omitempty"`
	// memory_card contract (SPEC-0085 R6): Op selects the write; Card/To
	// name cards, Evidence anchors a fact to a captured entry, Tags sets
	// frontmatter on created cards.
	Op       string   `json:"op,omitempty"`
	Card     string   `json:"card,omitempty"`
	Evidence string   `json:"evidence,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}
type Tools struct {
	Workspace, Archive, Organization *os.Root
	HTTP                             *http.Client
	HHURL, HHKey, UserAgent          string
	HHEnabled, OrgScoped             bool
	OrgActions                       map[string]bool
	ReadOnlyTools                    map[string]bool
	// ReadOnlyPaths lists workspace-relative directory prefixes the file
	// tools refuse to write: inbox originals, shared mounts and vault
	// memory land here, rendered from the workspace passport.
	ReadOnlyPaths []string
	// BoundaryPath is the mounted /config/workspace.yaml the file tools
	// reload for the active writable/read_only contract. Managed startup
	// requires it; stateless component callers may omit it.
	BoundaryPath string
	// ToolPolicyPath is the rendered snapshot location (HUB_TOOL_POLICY):
	// toolPolicyDeny reloads it per call so a revoke lands on the next call,
	// not the next respawn (R5.2 grant epoch).
	ToolPolicyPath string
	// ToolPolicy is the rendered tool-governance extract (HUB_TOOL_POLICY):
	// deny/deny-all/refuse-read only decisions compiled per capability
	// family. Nil on unmanaged runtimes — the env pin is only rendered with
	// the managed mount.
	ToolPolicy        *api.ToolPolicySnapshot
	StateDir          string
	ToolHub           *toolhub.Store
	ToolHubAuth       identity.Envelope
	SSH               *sshcap.Service
	SSHGrants         sshcap.Grants
	sshErr            error
	Exposure          *exposure.Manager
	exposureErr       error
	exposureProvider  exposure.Provider
	Restart           func() error
	CommunicationURL  string
	CommunicationAuth string
	// Skill paths: SkillsEffective is the read-only mounted effective view
	// (/skills), SkillsStore the writable owner store (HUB_SKILLS_STORE),
	// SkillsOrg the writable org store when org-bound (HUB_SKILLS_ORG).
	SkillsEffective, SkillsStore, SkillsOrg string
	// Skills is the effective view opened as a read-only executor root —
	// the skills: logical root for file/document/image ops (R7.3).
	Skills        *os.Root
	Media         *media.Session
	browserMu     sync.Mutex
	browserRuns   map[string]*browserRun
	mu            sync.Mutex
	lock, envLock *flock.Flock
}

var signalRuntime = func(process *os.Process) {
	time.Sleep(250 * time.Millisecond)
	_ = process.Signal(os.Interrupt)
}

func Open(workspace, archive string, organization ...string) (*Tools, error) {
	if len(organization) > 1 {
		return nil, fmt.Errorf("one organization root allowed")
	}
	org := ""
	if len(organization) == 1 {
		org = organization[0]
	}
	return OpenRoots(workspace, archive, org)
}

// OpenRoots opens the filesystem roots the executor owns. archive and
// organization may be absent (managed runtimes mount workspace only); the
// handlers then answer "root is not configured" for that domain.
func OpenRoots(workspace, archive, organization string) (*Tools, error) {
	w, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	var a, o *os.Root
	if archive != "" {
		a, err = os.OpenRoot(archive)
		if err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	if organization != "" {
		o, err = os.OpenRoot(organization)
		if err != nil {
			_ = w.Close()
			if a != nil {
				_ = a.Close()
			}
			return nil, err
		}
	}
	stateDir := os.Getenv("HUB_STATE")
	if stateDir == "" {
		stateDir = "/state"
	}
	toolHubStore, toolHubAuth, err := loadToolHub(stateDir)
	if err != nil {
		_ = w.Close()
		if a != nil {
			_ = a.Close()
		}
		if o != nil {
			_ = o.Close()
		}
		return nil, err
	}
	communicationAuth := os.Getenv("HUB_COMMUNICATION_AUTH")
	if communicationAuth == "" {
		communicationAuth = os.Getenv("HUB_RUNTIME_AUTH")
	}
	toolPolicy, err := loadToolPolicy()
	if err != nil {
		_ = w.Close()
		if a != nil {
			_ = a.Close()
		}
		if o != nil {
			_ = o.Close()
		}
		return nil, err
	}
	// The effective view doubles as the read-only skills: executor root
	// (R7.3); an absent mount leaves it nil — handlers then answer
	// "skills root is not configured" instead of failing Open.
	var sk *os.Root
	if eff := os.Getenv("HUB_SKILLS_EFFECTIVE"); eff != "" {
		sk, _ = os.OpenRoot(eff)
	}
	boundaryPath := strings.TrimSpace(os.Getenv("HUB_WORKSPACE_BOUNDARY"))
	explicitBoundary := boundaryPath != ""
	managed := os.Getenv("HUB_PASSPORT_REVISION") != "" || os.Getenv("HUB_CAPABILITY_MODE") == "managed"
	if boundaryPath == "" {
		boundaryPath = "/config/workspace.yaml"
	}
	if !managed && !explicitBoundary {
		if _, err := os.Lstat(boundaryPath); os.IsNotExist(err) {
			boundaryPath = ""
		}
	}
	t := &Tools{Workspace: w, Archive: a, Organization: o, Skills: sk, OrgScoped: o != nil, OrgActions: parseActions(os.Getenv("HUB_ORG_ACTIONS")), ReadOnlyTools: readOnlyToolNames(parseActions(os.Getenv("HUB_TOOLS_RO"))), ReadOnlyPaths: readOnlyPathList(os.Getenv("HUB_WORKSPACE_RO")), BoundaryPath: boundaryPath, ToolPolicyPath: strings.TrimSpace(os.Getenv("HUB_TOOL_POLICY")), ToolPolicy: toolPolicy, StateDir: stateDir, ToolHub: toolHubStore, ToolHubAuth: toolHubAuth, Restart: func() error { return restartRuntime(stateDir) }, CommunicationURL: strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_CONTROL_URL")), CommunicationAuth: communicationAuth, SkillsEffective: os.Getenv("HUB_SKILLS_EFFECTIVE"), SkillsStore: os.Getenv("HUB_SKILLS_STORE"), SkillsOrg: os.Getenv("HUB_SKILLS_ORG"), Media: media.New(w), lock: flock.New(filepath.Join(workspace, ".hub-writer.lock")), envLock: flock.New(filepath.Join(stateDir, ".self-env.lock")), HHURL: "https://api.hh.ru", HHKey: os.Getenv("HH_TOKEN"), UserAgent: os.Getenv("HH_USER_AGENT"), HHEnabled: os.Getenv("HUB_HH_ENABLED") == "true", HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if _, err := t.boundary(); err != nil {
		t.Close()
		return nil, err
	}
	t.openSSH()
	t.openExposure()
	return t, nil
}

// readOnlyToolNames expands the HUB_TOOLS_RO capability list into the tool
// names the executor must refuse: every write-effect tool of a read-only
// capability. The env arrives only from the rendered runtime spec — the
// agent cannot widen it — and admission profiles remain the authority; this
// is the same answer enforced inside the process.
func readOnlyToolNames(capabilities map[string]bool) map[string]bool {
	if len(capabilities) == 0 {
		return nil
	}
	names := map[string]bool{}
	for _, spec := range HubToolsDefinition().Tools {
		family, _, _ := strings.Cut(spec.CapabilityID, ".")
		if spec.Effect == api.WriteEffect && (capabilities[spec.CapabilityID] || capabilities[family]) {
			names[spec.Name] = true
		}
	}
	return names
}

// loadToolPolicy reads the rendered governance extract. The env pin arrives
// only with the managed /config mount, so a set pin with a missing or
// corrupt file is a hard failure — the executor refuses to start rather than
// guessing at authority.
func loadToolPolicy() (*api.ToolPolicySnapshot, error) {
	path := strings.TrimSpace(os.Getenv("HUB_TOOL_POLICY"))
	if path == "" {
		return nil, nil
	}
	policy, err := api.LoadToolPolicy(path)
	if err != nil {
		return nil, fmt.Errorf("tool policy: %w", err)
	}
	return policy, nil
}

func loadToolHub(stateDir string) (*toolhub.Store, identity.Envelope, error) {
	path := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_STORE"))
	if path == "" {
		return nil, identity.Envelope{}, nil
	}
	store, err := toolhub.Load(path)
	if err != nil {
		return nil, identity.Envelope{}, fmt.Errorf("load ToolHub catalog: %w", err)
	}
	auth := runtimeEnvelope()
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, identity.Envelope{}, fmt.Errorf("ToolHub identity: %w", err)
	}
	store.Reconnect = &toolhub.ReconnectController{Store: store, Auth: auth, OnChange: func(change api.ProjectionChange) error {
		return api.WriteReconnectMarker(stateDir, change)
	}}
	return store, auth, nil
}

func toolHubEnvOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// runtimeEnvelope is the principal identity of this runtime process; ToolHub
// and the SSH capability both present it to Credential Broker.
func runtimeEnvelope() identity.Envelope {
	principal := os.Getenv("HUB_PRINCIPAL_ID")
	if principal == "" {
		principal = os.Getenv("HUB_USER_ID")
	}
	contextID := os.Getenv("HUB_CONTEXT_ID")
	if contextID == "" {
		contextID = principal
	}
	runtimeID := os.Getenv("HUB_RUNTIME_ID")
	if runtimeID == "" {
		runtimeID = principal
	}
	return identity.Envelope{Schema: identity.Schema, PrincipalID: principal, ExternalIdentityID: toolHubEnvOr("HUB_EXTERNAL_ID", principal), ContextID: contextID, RuntimeID: runtimeID, ConversationID: toolHubEnvOr("HUB_CONVERSATION_ID", "toolhub"), DeliveryTargetID: toolHubEnvOr("HUB_DELIVERY_TARGET_ID", "toolhub"), PolicyVersion: toolHubEnvOr("HUB_POLICY_VERSION", "policy-1")}
}
func (t *Tools) Close() {
	t.browserMu.Lock()
	for id, run := range t.browserRuns {
		_ = run.session.Close()
		delete(t.browserRuns, id)
	}
	t.browserMu.Unlock()
	if t.SSH != nil {
		t.SSH.Close()
	}
	if t.Exposure != nil {
		t.Exposure.Shutdown()
	}
	if p, ok := t.exposureProvider.(interface{ Shutdown() }); ok {
		p.Shutdown()
	}
	_ = t.Workspace.Close()
	if t.Archive != nil {
		_ = t.Archive.Close()
	}
	if t.Organization != nil {
		_ = t.Organization.Close()
	}
	if t.Skills != nil {
		_ = t.Skills.Close()
	}
}

// hasParam reports whether a structured `any` argument carries a value —
// either a non-empty legacy JSON string or a decoded object/array.
func hasParam(v any) bool {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return v != nil
}

// jsonParam decodes one structured call argument into out. The r5 schema
// sends real objects/arrays; the older internal caller convention passed
// the same JSON as a string — both forms decode identically here.
func jsonParam(v any, out any) error {
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return json.Unmarshal([]byte(s), out)
	}
	if v == nil {
		return nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
