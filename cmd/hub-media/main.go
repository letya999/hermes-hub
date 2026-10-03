// Command hub-media is the standalone media-generation service for Hermes
// Hub: synchronous image generation/edits plus durable async video jobs.
// Provider credentials live only here; runtimes reach it over the internal
// network with a bearer token. Engines: remote (OpenAI-compatible upstream)
// or fal (fal.run sync + queue.fal.run jobs) via HUB_MEDIA_ENGINE.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/mediasvc"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "health" {
		req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8090/healthz", nil)
		if err != nil {
			log.Fatalf("health: %v", err)
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	cfg, err := mediasvc.ConfigFromEnv(mediasvc.RoleMedia)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	srv, err := newServer(cfg)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("hub-media engine=%s listen=%s", cfg.Engine, cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

func newServer(cfg mediasvc.Config) (*http.Server, error) {
	handler, err := mediasvc.Serve(cfg)
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}, nil
}
