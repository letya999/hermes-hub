package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hub-toolhub:", err)
		os.Exit(1)
	}
}

func run() error {
	storePath := os.Getenv("HUB_TOOLHUB_STORE")
	if storePath == "" {
		return errors.New("HUB_TOOLHUB_STORE is required")
	}
	config, err := toolhub.EndpointConfigFromEnv()
	if err != nil {
		return err
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		return fmt.Errorf("load ToolHub store: %w", err)
	}
	handler, err := toolhub.NewEndpointHandler(config, store)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: config.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	refresher := handler.(interface{ RefreshProjection() error })
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		pending := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := refresher.RefreshProjection(); err != nil {
					if !pending {
						fmt.Fprintln(os.Stderr, "ToolHub projection refresh pending")
					}
					pending = true
				} else {
					pending = false
				}
			}
		}
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
