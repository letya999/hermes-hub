package runtime

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
	"github.com/letya999/hermes-hub/internal/identity"
)

// MaxPromptBytes bounds one admitted prompt body and one stream frame; every
// hop of the private contract decodes under the same ceiling.
const MaxPromptBytes = 2 * 1024 * 1024

// RunStreamContentType is the framed run-event stream the supervisor consumes.
const RunStreamContentType = "application/x-ndjson"

const RunAuthorizationPath = "/v1/jobs/authorization"
const RunDeliveryHeader = "X-Hub-Run-Delivery"

const KubernetesIsolationPath = "/run/hub-isolation/policy.json"

// KubernetesIsolationReceipt is a controller-authored private input, never an
// assertion accepted from the agent or from an environment flag alone.
type KubernetesIsolationReceipt struct {
	Namespace    string `json:"namespace"`
	Pod          string `json:"pod"`
	Generation   string `json:"generation"`
	ContextHash  string `json:"context_hash"`
	PolicyUID    string `json:"policy_uid"`
	PolicySHA256 string `json:"policy_sha256"`
	RelayIP      string `json:"relay_ip"`
}

// RunAuthorization is a private, in-memory handoff after Hermes admission.
type RunAuthorization struct {
	JobID      string            `json:"job_id"`
	RunID      string            `json:"run_id"`
	Generation string            `json:"generation"`
	Tokens     map[string]string `json:"tokens"`
}

// Connector installation is an isolated build, not an ordinary chat turn.
// Keep the supervisor observing long enough for the ToolHub build ceiling;
// Hermes itself sends the user periodic heartbeats while it waits.
// The cap is the single run budget every hop shares (RunBudget).

// ExecuteRequest is the private communication-hub to runtime contract.
type ExecuteRequest struct {
	identity.Envelope
	OperationalEpoch       uint64 `json:"operational_epoch,omitempty"`
	JobID                  string `json:"job_id"`
	OrganizationID         string `json:"organization_id"`
	UserID                 string `json:"user_id"`
	ActorID                string `json:"actor_id"`
	ScopeID                string `json:"scope_id"`
	Channel                string `json:"channel"`
	Trigger                string `json:"trigger"`
	IdempotencyKey         string `json:"idempotency_key"`
	Text                   string `json:"text"`
	InboundID              string `json:"inbound_id,omitempty"`
	Sensitive              bool   `json:"sensitive,omitempty"`
	Untrusted              bool   `json:"untrusted,omitempty"`
	Assignment             bool   `json:"assignment,omitempty"`
	TaskID                 string `json:"task_id,omitempty"`
	AssignmentID           string `json:"assignment_id,omitempty"`
	AssignmentPlanRevision uint64 `json:"assignment_plan_revision,omitempty"`
	Probe                  bool   `json:"probe,omitempty"`
	// Instructions is the hub-pinned presentation guidance for this admitted
	// run (ephemeral system prompt upstream). It is never a synthetic user
	// message and never carries authorization.
	Instructions string `json:"instructions,omitempty"`
}

// SelfEnvRequest is the private protected-form-to-runtime contract. It is
// never sent through Hermes jobs or exposed as a model tool.
type SelfEnvRequest struct {
	identity.Envelope
	OrganizationID string            `json:"organization_id"`
	UserID         string            `json:"user_id"`
	ActorID        string            `json:"actor_id"`
	ScopeID        string            `json:"scope_id"`
	Values         map[string]string `json:"values"`
}

// SkillApplyRequest is the protected-card-to-runtime contract for skill draft
// publish/rollback (SPEC-0083 R5.3). It is never sent through Hermes jobs or
// exposed as a model tool; the supervisor uses the envelope fields only to
// route to the owning runtime, the runtime consumes the apply fields.
type SkillApplyRequest struct {
	identity.Envelope
	OrganizationID string `json:"organization_id"`
	UserID         string `json:"user_id"`
	ActorID        string `json:"actor_id"`
	ScopeID        string `json:"scope_id"`
	Action         string `json:"action"`
	DraftID        string `json:"draft_id"`
	SkillName      string `json:"skill_name"`
	Scope          string `json:"scope"`
	BaseRevision   string `json:"base_revision"`
	Approver       string `json:"approver"`
}

// SkillCredsRequest is the protected-form-to-runtime contract for skill
// credential delivery (SPEC-0084 R8.2). The supervisor uses the envelope
// fields only to route to the owning runtime, which consumes skill+values.
type SkillCredsRequest struct {
	identity.Envelope
	OrganizationID string            `json:"organization_id"`
	UserID         string            `json:"user_id"`
	ActorID        string            `json:"actor_id"`
	ScopeID        string            `json:"scope_id"`
	Skill          string            `json:"skill"`
	Values         map[string]string `json:"values"`
}

type ExecuteResponse struct {
	Text              string   `json:"text"`
	JobID             string   `json:"job_id,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	RunID             string   `json:"run_id,omitempty"`
	RuntimeGeneration string   `json:"runtime_generation,omitempty"`
	Status            string   `json:"status,omitempty"`
	LastEvent         string   `json:"last_event,omitempty"`
	EventID           string   `json:"event_id,omitempty"`
	ToolName          string   `json:"tool_name,omitempty"`
	ApprovalID        string   `json:"approval_id,omitempty"`
	ApprovalChoices   []string `json:"approval_choices,omitempty"`
	// ApprovalSummary is the redacted human-readable context of the flagged
	// action (what is being approved), bounded at the runtime boundary.
	ApprovalSummary string        `json:"approval_summary,omitempty"`
	Artifacts       []ArtifactRef `json:"artifacts,omitempty"`
	// Voice carries the text the model asked to deliver as a voice message
	// (upstream VOICE: marker). The gateway synthesizes and sends it; the
	// audio itself is never persisted — the text transcript stays canonical.
	Voice string `json:"voice,omitempty"`
}

type RunReference struct {
	ExecuteRequest
	RunID             string `json:"run_id"`
	SessionID         string `json:"session_id"`
	RuntimeGeneration string `json:"runtime_generation"`
}

// RunControl addresses an existing run; it never admits new work.
type RunControl struct {
	Reconcile bool `json:"reconcile,omitempty"`
	RunReference
	Action    string    `json:"action"`
	RequestID string    `json:"request_id,omitempty"`
	Choice    string    `json:"choice,omitempty"`
	Deadline  time.Time `json:"deadline,omitempty"`
}

// GoalRequest is the /v1/goal body: the trusted envelope plus one verb. The
// session is always derived from the envelope — a caller can never name
// another conversation's goal. Op is one of set|status|pause|resume|clear|
// evaluate; out-of-scope upstream verbs are refused by name on both engines
// (SPEC-0074).
type GoalRequest struct {
	ExecuteRequest
	Op     string `json:"op"`
	Text   string `json:"text,omitempty"`
	Engine string `json:"engine,omitempty"`
	// ExpectEngine refuses the request when the resolved engine differs —
	// an active goal pinned to engine A must not silently run under B.
	ExpectEngine string `json:"expect_engine,omitempty"`
	JudgeMode    string `json:"judge_mode,omitempty"`
	Goal         string `json:"goal,omitempty"`
	Response     string `json:"response,omitempty"`
}

// GoalResult is the bounded projection returned to the gateway. No
// transcript or free-form model text crosses — verdict reasons and evidence
// excerpts are capped before they leave the runtime.
type GoalResult struct {
	Engine             string `json:"engine,omitempty"`
	Judge              string `json:"judge,omitempty"`
	Verdict            string `json:"verdict,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Evidence           string `json:"evidence,omitempty"`
	Status             string `json:"status,omitempty"`
	Goal               string `json:"goal,omitempty"`
	TurnsUsed          int    `json:"turns_used,omitempty"`
	MaxTurns           int    `json:"max_turns,omitempty"`
	ShouldContinue     bool   `json:"should_continue,omitempty"`
	KickoffPrompt      string `json:"kickoff_prompt,omitempty"`
	ContinuationPrompt string `json:"continuation_prompt,omitempty"`
	Output             string `json:"output,omitempty"`
	ParseFailure       bool   `json:"parse_failure,omitempty"`
	TransportFailure   bool   `json:"transport_failure,omitempty"`
	FailureClass       string `json:"failure_class,omitempty"`
}

// UsageRequest resolves measured session usage on the runtime that owns the
// envelope's session. Resident runtimes ignore the envelope; the supervisor
// routes on it exactly like /v1/artifact.
type UsageRequest struct {
	ExecuteRequest
	LookupOnly bool `json:"lookup_only,omitempty"`
}

// SessionUsage is the measured usage view for one durable Hermes session.
// Pointer fields stay nil — and serialize as null — whenever the pinned
// upstream API carries no authoritative value; consumers must render null as
// "unknown" and never estimate from transcript text or billed totals.
type SessionUsage struct {
	// DurationMS is the measured gateway attempt wall time, not session age.
	DurationMS        *int64    `json:"duration_ms,omitempty"`
	Object            string    `json:"object"`
	DeclaredSessionID string    `json:"declared_session_id"`
	SessionID         string    `json:"session_id"`
	SessionFound      bool      `json:"session_found"`
	Source            string    `json:"source"`
	FetchedAt         time.Time `json:"fetched_at"`
	Model             string    `json:"model,omitempty"`
	Title             string    `json:"title,omitempty"`
	StartedAt         string    `json:"started_at,omitempty"`
	InputTokens       *int64    `json:"input_tokens"`
	OutputTokens      *int64    `json:"output_tokens"`
	CacheReadTokens   *int64    `json:"cache_read_tokens"`
	CacheWriteTokens  *int64    `json:"cache_write_tokens"`
	ReasoningTokens   *int64    `json:"reasoning_tokens"`
	APICallCount      *int64    `json:"api_call_count"`
	MessageCount      *int64    `json:"message_count"`
	ToolCallCount     *int64    `json:"tool_call_count"`
	EstimatedCostUSD  *float64  `json:"estimated_cost_usd"`
	ActualCostUSD     *float64  `json:"actual_cost_usd"`
	// Current-prompt context and the model window are distinct from cumulative
	// billing counters. The pinned API exposes neither, so they stay null.
	ContextTokens *int64 `json:"context_tokens"`
	ContextWindow *int64 `json:"context_window"`
	// Compactions counts measured session rotations along the durable
	// parent_session_id lineage; LastCompactionAt is the retired parent's
	// ended_at. In-place compactions leave no row and are not fabricated.
	Compactions      *int64 `json:"compactions"`
	LastCompactionAt string `json:"last_compaction_at,omitempty"`
	// CompactionsTruncated marks the lineage walk stopped at the hop cap with
	// more ancestors present; consumers render Compactions as a lower bound
	// (≥N) instead of an exact count.
	CompactionsTruncated bool `json:"compactions_truncated,omitempty"`
	// InPlaceCompactions and LastCompaction come only from the compaction
	// journal — transcript-carrier compactions leave no lineage row, so they
	// surface here or nowhere. Nil count means the journal recorded none.
	InPlaceCompactions *int64            `json:"in_place_compactions"`
	LastCompaction     *compaction.Event `json:"last_compaction,omitempty"`
	LastActive         string            `json:"last_active,omitempty"`
}

// CompactRequest is the /v1/compact body: the same trusted envelope the run
// endpoints carry plus the parsed upstream argument vocabulary. The session
// is always derived from the envelope — a caller can never name another
// conversation's transcript.
type CompactRequest struct {
	ExecuteRequest
	Here       int    `json:"here,omitempty"`
	Focus      string `json:"focus,omitempty"`
	Prune      bool   `json:"prune,omitempty"`
	Preview    bool   `json:"preview,omitempty"`
	Engine     string `json:"engine,omitempty"`
	BusyPolicy string `json:"busy_policy,omitempty"`
}

// ArtifactRef is one generated deliverable under workspace/artifacts. Path is
// relative to the artifacts root ("documents/report.pdf"). Blob and Error are
// populated only on the gateway side after the bytes crossed this contract.
type ArtifactRef struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	Mime  string `json:"mime,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Blob  string `json:"blob,omitempty"`
	Error string `json:"error,omitempty"`
}

// ArtifactRequest fetches one artifact from the runtime owning the envelope.
// Resident runtimes ignore the envelope; the supervisor uses it to route.
// Consume asks the runtime to remove the workspace file once its bytes have
// been served: artifacts are hand-off storage, not permanent workspace copies.
type ArtifactRequest struct {
	ExecuteRequest
	Name    string `json:"name"`
	Consume bool   `json:"consume,omitempty"`
}

// RuntimeHealth contains observations only, never credentials or native details.
// Native platform health does not certify lazy MCP/provider connections.
type RuntimeHealth struct {
	HermesReadiness     string `json:"hermes_readiness"`
	ConnectorHealth     string `json:"connector_health"`
	Connected           int    `json:"connected"`
	Degraded            int    `json:"degraded"`
	Unknown             int    `json:"unknown"`
	ExternalConnections string `json:"external_connections"`
	// SPEC-0053 R1.4-R1.6: the rendered manifest's expectation vs the served
	// surface, names nothing serves, per-surface reasons, and the provider
	// catalog verdict for the configured model. Observation only — a degraded
	// surface never restarts the runtime.
	ToolSurface  string            `json:"tool_surface"`
	MissingTools []string          `json:"missing_tools,omitempty"`
	ToolReasons  map[string]string `json:"tool_reasons,omitempty"`
	ModelStatus  string            `json:"model_status"`
}

// RuntimeStatus is the bounded JSON contract of GET /v1/status — fixed
// allowlisted fields only; the supervisor decodes the same shape.
type RuntimeStatus struct {
	RequestsTotal int64 `json:"requests_total"`
	HTTP5xxTotal  int64 `json:"http_5xx_total"`
	LatencyP95MS  int64 `json:"latency_p95_ms"`
	Samples       int   `json:"samples"`
	ActiveRuns    int   `json:"active_runs"`
	MCPInflight   int64 `json:"mcp_inflight"`
}

// SessionReadRequest is admitted by communication's task mapping before the
// supervisor routes it. The runtime still fences the owner and session shape.
type SessionReadRequest struct {
	ExecuteRequest
	SessionID string `json:"session_id"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
}

func ValidSessionReadRequest(request SessionReadRequest) bool {
	return request.Envelope.Validate(request.PrincipalID, request.ContextID, request.RuntimeID, request.PolicyVersion) == nil &&
		request.UserID == request.PrincipalID && request.ScopeID == "user:"+request.PrincipalID &&
		validHubSessionID(request.SessionID) && request.Offset >= 0 && request.Offset <= 100000 && request.Limit >= 1 && request.Limit <= 50
}

// ScriptRequest uses the ordinary owner envelope without starting Hermes.
type ScriptRequest struct {
	ExecuteRequest
	DueAt time.Time `json:"due_at"`
	// Input is the bounded fetch/extract fragment passed to input() inside
	// the probe script (SPEC-0059).
	Input string `json:"input,omitempty"`
	// MountSpace mounts the owner's spaces/<user>/watch directory read-only
	// at /watch for file-state probes. Never caller-supplied paths.
	MountSpace bool `json:"mount_space,omitempty"`
	// Needs carries hub-prefetched read-tool results into the isolated
	// runtime as the needs() builtin (SPEC-0059 R5.2). Max four entries,
	// each bounded; the container still has no network or tool access.
	Needs []string `json:"needs,omitempty"`
}

// ReadRunStream bounds each normalized frame and the total connection output.
// Synchronous consumption propagates backpressure rather than growing a queue.
func ReadRunStream(reader io.Reader, consume func(ExecuteResponse) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxPromptBytes+64*1024)
	total := 0
	for scanner.Scan() {
		total += len(scanner.Bytes())
		if total > 4*MaxPromptBytes {
			return errors.New("runtime stream output limit exceeded")
		}
		var event ExecuteResponse
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return errors.New("invalid runtime stream frame")
		}
		if err := consume(event); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func validHubSessionID(id string) bool {
	if len(id) != len("hub-")+24 || !strings.HasPrefix(id, "hub-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "hub-"))
	return err == nil
}
