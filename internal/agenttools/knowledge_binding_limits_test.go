package agenttools

import (
	"fmt"
	"testing"
)

func TestKnowledgeBindingAggregateLimits(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count, size int
	}{{"count", 129, 1}, {"bytes", 4, (8 << 20) + 1}} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string][]byte{}
			for i := 0; i < tc.count; i++ {
				files[fmt.Sprintf("doc-%d", i)] = make([]byte, tc.size)
			}
			if _, err := StageKnowledgeSource(t.TempDir(), "alice", "project", "docs", "present", files); err == nil {
				t.Error("aggregate knowledge limit not enforced")
			}
		})
	}
}
