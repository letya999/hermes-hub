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
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "spawn-controller:", err)
		os.Exit(1)
	}
}
