package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/letya999/hermes-hub/internal/communication"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	config, err := communication.ConfigFromEnv()
	if err == nil {
		var gateway *communication.Gateway
		gateway, err = communication.New(config)
		if err == nil {
			err = gateway.Run(ctx)
		}
	}
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "communication-hub:", err)
		os.Exit(1)
	}
}
