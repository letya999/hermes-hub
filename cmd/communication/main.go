package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
			if addr := strings.TrimSpace(config.ListenAddr); addr != "" {
				server := &http.Server{Addr: addr, Handler: gateway.Handler(), ReadHeaderTimeout: 10 * time.Second}
				go func() { _ = server.ListenAndServe() }()
				defer func() {
					shutdownCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
					defer stop()
					_ = server.Shutdown(shutdownCtx)
				}()
			}
			err = gateway.Run(ctx)
		}
	}
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "communication:", err)
		os.Exit(1)
	}
}
