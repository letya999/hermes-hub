package runtime

import (
	"context"
	"io"
	"time"
)

// RunBudget is the single maximum duration of one job across every hop of the
// execution chain (HUB_RUN_BUDGET, default 30 min). No hop may cut a run
// earlier; non-stream calls keep their own bounded timeouts instead.
func RunBudget() time.Duration { return durationEnv("HUB_RUN_BUDGET", 30*time.Minute) }

// StreamIdleDeadline is the silence window that abandons a run stream hop
// (HUB_STREAM_IDLE, default 60 s). Heartbeats and events both reset it.
func StreamIdleDeadline() time.Duration { return durationEnv("HUB_STREAM_IDLE", time.Minute) }

// IdleReader cancels cancel when the wrapped reader yields no bytes for idle.
// The timer stops once the stream ends or errors.
func IdleReader(r io.Reader, idle time.Duration, cancel context.CancelFunc) io.Reader {
	return &idleReader{r: r, idle: idle, timer: time.AfterFunc(idle, cancel)}
}

type idleReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if err != nil {
		i.timer.Stop()
	} else if n > 0 {
		i.timer.Reset(i.idle)
	}
	return n, err
}
