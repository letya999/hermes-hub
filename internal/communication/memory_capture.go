package communication

import (
	"os"

	"github.com/letya999/hermes-hub/internal/commodel"
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
