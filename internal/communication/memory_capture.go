package communication

import (
	"github.com/letya999/hermes-hub/internal/memory"
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
