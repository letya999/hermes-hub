package memory

import "testing"

func TestPromotionGate(t *testing.T) {
	cases := []struct {
		entry Entry
		want  bool
	}{
		{Entry{Kind: KindInteractive, Provenance: ProvenanceOwner}, true},
		{Entry{Kind: KindInteractive, Provenance: ProvenanceAgent}, true},
		{Entry{Kind: KindInteractive, Provenance: ProvenanceUntrusted}, false},
		{Entry{Kind: KindInteractive, Provenance: ProvenanceSystem}, false},
		{Entry{Kind: KindRoutine, Provenance: ProvenanceOwner}, false},
		{Entry{Kind: KindAssignment, Provenance: ProvenanceOwner}, false},
		{Entry{Kind: KindProbe, Provenance: ProvenanceOwner}, false},
		{Entry{Kind: KindGoal, Provenance: ProvenanceOwner}, false},
		{Entry{Kind: KindCompaction, Provenance: ProvenanceOwner}, false},
		{Entry{Kind: KindRoutine, Provenance: ProvenanceUntrusted}, false},
	}
	for i, tc := range cases {
		if got := tc.entry.PromotionEligible(); got != tc.want {
			t.Errorf("case %d: PromotionEligible=%v want %v (%s/%s)", i, got, tc.want, tc.entry.Kind, tc.entry.Provenance)
		}
	}
}
