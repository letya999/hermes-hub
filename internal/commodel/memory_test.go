package commodel

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/memory"
)

// SPEC-0085 R2.3/R2.4: the job carries the hub-stamped fields memory capture
// classifies from — forwarded owner text is untrusted+interactive, a probe
// schedule is a probe kind, and scope decides the audience slice.
func TestJobMemoryClassification(t *testing.T) {
	owner := Job{Trigger: "message", ScopeID: "user:u1", Untrusted: true}
	if got := owner.MemoryKind(); got != memory.KindInteractive {
		t.Fatalf("forwarded owner turn kind = %s", got)
	}
	if got := owner.MemoryTurnProvenance(); got != memory.ProvenanceUntrusted {
		t.Fatalf("forwarded owner turn prov = %s", got)
	}
	if got := owner.MemoryAudience(); got != memory.AudiencePrivate {
		t.Fatalf("user: scope audience = %s", got)
	}
	probe := Job{Trigger: "cron", ScopeID: "user:u1", Probe: true}
	if got := probe.MemoryKind(); got != memory.KindProbe {
		t.Fatalf("probe schedule kind = %s", got)
	}
	group := Job{Trigger: "message", ScopeID: "group:u1"}
	if got := group.MemoryAudience(); got != memory.AudienceGroup {
		t.Fatalf("group: scope audience = %s", got)
	}
}
