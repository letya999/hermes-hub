// Command hub-stt is the standalone speech-to-text service for Hermes Hub:
// OpenAI-compatible /v1/audio/transcriptions plus a durable async job API for
// long recordings from uploads, mounted storage or presigned https sources.
// The recognition engine is swappable via HUB_MEDIA_ENGINE
// (command/remote/sherpa).
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
	cfg, err := mediasvc.ConfigFromEnv(mediasvc.RoleSTT)
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
	log.Printf("hub-stt engine=%s listen=%s", cfg.Engine, cfg.ListenAddr)
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
		ReadTimeout:       10 * time.Minute,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}, nil
}
