//go:build integration

package communication

import "context"

// ProcessControlProbe exercises the production durable dispatcher without a
// Telegram account or a second execution worker claiming the probe's job.
func ProcessControlProbe(ctx context.Context, spool *Spool, config Config) {
	g := Gateway{spool: spool, config: config}
	g.controlOne(ctx)
}
