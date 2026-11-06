package communication

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/memory"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Memory capture (SPEC-0098 M4): non-sensitive accepted turns and completed
// runs enter the durable outbox; ToolHub classifies and commits them.

// captureTurn is the spool's onAccepted hook: it runs after the job file is
// durable and before Enqueue returns — persist-before-ack. The event lands
// in the durable outbox; ToolHub is the only vault writer (SPEC-0098 M4).
// Every enrolled principal produces events — the endpoint resolves that
// principal's own vault, so a sibling's turn feeds the sibling's lane.
func (g *Gateway) captureTurn(job Job) error {
	if job.Sensitive {
		return nil
	}
	if err := g.spool.writeCaptureLocked(captureEventForTurn(job)); err != nil {
		return err
	}
	g.kickCapture()
	return nil
}

// captureReply records the completed-run fact: the durable outbox event
// carries the run id from the terminal outcome, and ToolHub verifies it
// against the lifecycle record before committing. Sensitive exchanges stay
// out of durable memory entirely — no event is ever minted for them.
func (g *Gateway) captureReply(job Job, outcome commodel.RunOutcome) error {
	if job.Sensitive || outcome.RunID == "" {
		return nil // sensitive exchanges stay out of durable memory entirely
	}
	if err := g.spool.EnqueueCapture(captureEventForReply(job, outcome, outcome.Text, g.now())); err != nil {
		return err
	}
	g.kickCapture()
	return nil
}

func (g *Gateway) captureCompletedLocked(job Job, outcome commodel.RunOutcome) error {
	if !job.CaptureExpected || job.Sensitive || outcome.RunID == "" {
		return nil
	}
	if settled, err := g.spool.captureSettled("reply-" + job.ID); err == nil && settled {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := g.spool.writeCaptureLocked(captureEventForReply(job, outcome, outcome.Text, g.now())); err != nil {
		return err
	}
	g.kickCapture()
	return nil
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
