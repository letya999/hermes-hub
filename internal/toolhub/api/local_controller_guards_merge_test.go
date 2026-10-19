package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

// RunLocalController binds loopback only with a private token; anything else
// fails before any listener or workload starts.
func TestRunLocalControllerGuards(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	token := strings.Repeat("a", 32)
	config := LocalControllerConfig{}
	for _, listen := range []string{"0.0.0.0:1", "localhost:1", "bad", ":1"} {
		if err := RunLocalController(ctx, config, listen, token); err == nil {
			t.Fatalf("non-loopback listen %q accepted", listen)
		}
	}
	for _, tok := range []string{"short", strings.Repeat("x", 32) + "\n"} {
		if err := RunLocalController(ctx, config, "127.0.0.1:1", tok); err == nil {
			t.Fatalf("weak token accepted")
		}
	}
	// A valid loopback/token pair still fails closed without a workload plan.
	if err := RunLocalController(ctx, config, "127.0.0.1:0", token); err == nil {
		t.Fatal("empty workload config must fail")
	}
}
