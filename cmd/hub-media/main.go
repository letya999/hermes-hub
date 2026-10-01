// Command hub-media runs one standalone speech service for Hermes Hub:
// HUB_MEDIA_ROLE=stt serves transcription (sync + async jobs for long
// recordings), HUB_MEDIA_ROLE=tts serves synthesis. Two processes scale and
// restart independently; the engine behind either role is swappable via
// HUB_MEDIA_ENGINE (remote/command/sherpa).
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
	cfg, err := mediasvc.ConfigFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	handler, err := mediasvc.Serve(cfg)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("hub-media role=%s engine=%s listen=%s", cfg.Role, cfg.Engine, cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
