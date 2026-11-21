package runtime

import (
	"errors"
	"testing"
	"time"
)

// ctxReader blocks until done closes, mirroring an HTTP body that fails once
// the request context is cancelled.
type ctxReader struct {
	done chan struct{}
}

func (c *ctxReader) Read(p []byte) (int, error) {
	<-c.done
	return 0, errors.New("cancelled")
}

func TestIdleReaderCancelsOnSilence(t *testing.T) {
	cancelled := make(chan struct{})
	reader := IdleReader(&ctxReader{done: cancelled}, 20*time.Millisecond, func() { close(cancelled) })
	errc := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 8))
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("read returned before cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle deadline never cancelled the stream")
	}
}

func TestRunBudgetSingleSource(t *testing.T) {
	if got := RunBudget(); got != 30*time.Minute {
		t.Fatalf("default budget %v", got)
	}
	t.Setenv("HUB_RUN_BUDGET", "7m")
	if got := RunBudget(); got != 7*time.Minute {
		t.Fatalf("env budget %v", got)
	}
}

func TestStreamIdleDeadlineFromEnv(t *testing.T) {
	if got := StreamIdleDeadline(); got != time.Minute {
		t.Fatalf("default idle deadline %v", got)
	}
	t.Setenv("HUB_STREAM_IDLE", "5s")
	if got := StreamIdleDeadline(); got != 5*time.Second {
		t.Fatalf("env idle deadline %v", got)
	}
}
