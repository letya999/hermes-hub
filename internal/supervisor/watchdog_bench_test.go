package supervisor

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// #326: one watchdog sweep over 20 owned runtimes — the production host
// ceiling — must stay cheap: one bounded stats call, bounded lock holds, no
// per-container docker chatter beyond the health probe.
func BenchmarkWatchdogSweep20(b *testing.B) {
	m, err := New(Config{
		SpacesRoot:  b.TempDir(),
		RuntimeAuth: "secret",
		Image:       "hermes:test",
		Command: func(_ context.Context, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "inspect" {
				return nil, os.ErrNotExist
			}
			return []byte("running"), nil
		},
		Probe: func(context.Context, string, string) error { return nil },
		Stats: func(_ context.Context, containers []string) (map[string]ContainerStat, error) {
			stats := make(map[string]ContainerStat, len(containers))
			for _, c := range containers {
				stats[c] = ContainerStat{CPUPercent: 1.5, MemBytes: 256 << 20}
			}
			return stats, nil
		},
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		b.Fatal(err)
	}
	m.mu.Lock()
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("p%d\x00ctx%d\x00gateway", i, i)
		m.items[key] = &runtimeEntry{
			Runtime: Runtime{PrincipalID: fmt.Sprintf("p%d", i), ContextID: fmt.Sprintf("ctx%d", i), RuntimeID: fmt.Sprintf("rt%d", i), RuntimeMode: "gateway", Generation: "gen-1", Container: fmt.Sprintf("c%d", i), Address: "http://127.0.0.1:1", State: Idle, Leases: 1, LastUsed: m.cfg.Now()},
			leases:  map[string]Lease{},
		}
	}
	m.mu.Unlock()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.sweep(ctx, m.cfg.Now().Add(time.Minute))
	}
}
