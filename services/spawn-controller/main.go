// spawn-controller ensures one harness-runtime child per (space, agent) and
// forwards space traffic to it. It is the v1 control-plane owner of runtime
// compute in process mode.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/api"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := api.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "spawn-controller:", err)
		os.Exit(1)
	}
}
