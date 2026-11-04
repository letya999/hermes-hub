package communication

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Memory capture (SPEC-0085 R2): every accepted turn and delivered reply is
// appended verbatim to the space's daily leaf, with provenance the hub
// stamps — never the model, never the transport body. Entry ids derive from
// the job id so a spool retry can never double-capture.

// captureTurn is the spool's onAccepted hook: it runs after the job file is
// durable and before Enqueue returns — persist-before-ack. The leaf write is
// synchronous so a kill after acceptance still finds the turn on disk.
func (g *Gateway) captureTurn(job Job) error {
	if g.memory == nil {
		return nil
	}
	return g.memory.Append(memory.Entry{
		ID:         "turn-" + job.ID,
		Kind:       job.MemoryKind(),
		Provenance: job.MemoryTurnProvenance(),
		Audience:   job.MemoryAudience(),
		SourceRef:  job.ID,
		Body:       job.Text,
		ObservedAt: job.CreatedAt,
	}, job.Channel)
}

// captureReply records the agent's response under the same job — provenance
// is always agent; kind follows the turn's trigger for audit pairing.
func (g *Gateway) captureReply(job Job, response string) error {
	if g.memory == nil || job.Sensitive {
		return nil // sensitive exchanges stay out of durable memory entirely
	}
	return g.memory.Append(memory.Entry{
		ID:         "reply-" + job.ID,
		Kind:       job.MemoryKind(),
		Provenance: memory.ProvenanceAgent,
		Audience:   job.MemoryAudience(),
		SourceRef:  job.ID,
		Body:       response,
		ObservedAt: g.now().UTC(),
	}, job.Channel)
}

// runScopeFile is the audience stamp for in-flight runs. The gateway
// writes it before dispatch; agent write paths are workspace-confined
// and the governed vault writer rejects the runtime/ zone, so the stamp
// cannot be lifted through the declared tool surface. Expiry bounds a
// crash-orphaned stamp to one run budget.
func runScopeFile(vaultDir string) string {
	return filepath.Join(vaultDir, "runtime", "run-scope", "current")
}

type runScopeMap struct {
	Runs map[string]struct {
		Audience   string `json:"audience"`
		Provenance string `json:"provenance"`
		Expires    string `json:"expires"`
	} `json:"runs"`
}

// stampRunScope records the dispatching job's audience. Failures are
// best-effort: a missing stamp fails the tool closed to "group".
func stampRunScope(vaultDir, jobID string, audience memory.Audience, provenance memory.Provenance) {
	if vaultDir == "" {
		return
	}
	dir := filepath.Join(vaultDir, "runtime", "run-scope")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	m := readRunScope(vaultDir)
	if m.Runs == nil {
		m.Runs = map[string]struct {
			Audience   string `json:"audience"`
			Provenance string `json:"provenance"`
			Expires    string `json:"expires"`
		}{}
	}
	expires := time.Now().Add(hubruntime.RunBudget() + 5*time.Minute).UTC().Format(time.RFC3339)
	m.Runs[jobID] = struct {
		Audience   string `json:"audience"`
		Provenance string `json:"provenance"`
		Expires    string `json:"expires"`
	}{Audience: string(audience), Provenance: string(provenance), Expires: expires}
	writeRunScope(vaultDir, m)
}

// clearRunScope removes the finished job's stamp so a stale private stamp
// can never widen a later group run's lane.
func clearRunScope(vaultDir, jobID string) {
	m := readRunScope(vaultDir)
	if _, ok := m.Runs[jobID]; !ok {
		return
	}
	delete(m.Runs, jobID)
	writeRunScope(vaultDir, m)
}

func readRunScope(vaultDir string) runScopeMap {
	var m runScopeMap
	data, err := os.ReadFile(runScopeFile(vaultDir))
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func writeRunScope(vaultDir string, m runScopeMap) {
	body, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := runScopeFile(vaultDir) + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err == nil {
		_ = os.Rename(tmp, runScopeFile(vaultDir))
	}
}
