package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

var removalRetryInterval = 30 * time.Second
var reconcileInterval = 30 * time.Second

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hub-toolhub:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWithContext(ctx)
}

func removalRetryLoop(ctx context.Context, interval time.Duration, resume func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := resume(ctx); err != nil {
				log.Printf("ToolHub removal reconciliation: %v", err)
			}
		}
	}
}

func runWithContext(ctx context.Context) error {
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
	ctx, stop := context.WithCancel(ctx)
	var background sync.WaitGroup
	defer func() { stop(); background.Wait() }()
	runBackground := func(work func()) {
		background.Add(1)
		go func() { defer background.Done(); work() }()
	}
	server := &http.Server{Addr: config.Listen, Handler: diagnostics.HTTP("toolhub", handler), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	publicServer, publicLn, err := publicServerFor(handler)
	if err != nil {
		return err
	}
	if publicServer != nil {
		defer func() { _ = publicServer.Shutdown(context.Background()) }()
		go func() {
			if err := publicServer.Serve(publicLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "hub-toolhub public listener:", err)
			}
		}()
	}
	remover := handler.(interface{ ResumeRemovals(context.Context) error })
	// A transient startup failure is retried and reported by the periodic loop.
	_ = remover.ResumeRemovals(ctx)
	runBackground(func() { removalRetryLoop(ctx, removalRetryInterval, remover.ResumeRemovals) })
	reconciler := handler.(interface{ Reconcile(context.Context) error })
	runBackground(func() { removalRetryLoop(ctx, reconcileInterval, reconciler.Reconcile) })
	if dir := os.Getenv("HUB_DIAGNOSTICS_DIR"); dir != "" {
		scope := diagnostics.Scope{
			Project:  os.Getenv("HUB_DIAGNOSTICS_PROJECT"),
			AgentNet: os.Getenv("HUB_DIAGNOSTICS_AGENT_NET"),
			Owner:    os.Getenv("HUB_DIAGNOSTICS_OWNER"),
		}
		runBackground(func() {
			diagnostics.Follow(ctx, filepath.Join(dir, "hermes-diagnostics.txt"), diagnostics.Docker, 15*time.Second, scope)
		})
	}
	refresher := handler.(interface{ RefreshProjection() error })
	restartNotify := api.RuntimeRestartNotifierFromEnv()
	runBackground(func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		pending := false
		ticks := 0
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
				// list_changed only reaches clients holding an open stream;
				// the restart marker guarantees the next session reload. The
				// revision snapshot reloads the store file, so it runs on a
				// slower cadence than the projection ticker.
				if restartNotify != nil && ticks%8 == 0 {
					if targets, err := store.ProjectionRevisions(); err == nil {
						if err := restartNotify.Notify(targets); err != nil {
							fmt.Fprintln(os.Stderr, "ToolHub restart notify:", err)
						}
					}
				}
				ticks++
			}
		}
	})
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

// publicServerFor builds the opt-in public listener (HUB_PUBLIC_LISTEN): it
// serves only the handler's human-action surface — /credentials, /confirm,
// /remove, /connect, /oauth/callback — never control or gateway APIs. With no
// listen address configured no second server exists.
func publicServerFor(handler http.Handler) (*http.Server, net.Listener, error) {
	addr := os.Getenv("HUB_PUBLIC_LISTEN")
	if addr == "" {
		return nil, nil, nil
	}
	public, ok := handler.(interface{ PublicHandler() http.Handler })
	if !ok {
		return nil, nil, errors.New("endpoint handler has no public surface")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	return &http.Server{Addr: addr, Handler: public.PublicHandler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}, ln, nil
}
