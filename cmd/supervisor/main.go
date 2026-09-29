package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/supervisor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hub-supervisor:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("hub-supervisor", flag.ContinueOnError)
	spaces := flags.String("spaces", "spaces", "root containing isolated context homes")
	image := flags.String("image", "hermes-hub:0.3.0-prod", "pinned Hermes runtime image")
	listen := flags.String("listen", envOr("HUB_SUPERVISOR_LISTEN", "127.0.0.1:8876"), "private supervisor listen address")
	auth := flags.String("auth", supervisorAuthFromEnv(), "shared private supervisor token")
	ttl := flags.Duration("warm-ttl", 5*time.Minute, "idle runtime retention")
	max := flags.Int("max-concurrent", 8, "maximum running context runtimes")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *auth == "" {
		return errors.New("HUB_SUPERVISOR_AUTH or --auth is required")
	}
	stopLog, err := diagnostics.HostLog(filepath.Join(filepath.Dir(*spaces), ".local", "supervisor.log"))
	if err != nil {
		return err
	}
	defer stopLog()
	m, err := supervisor.New(supervisor.Config{SpacesRoot: *spaces, Image: *image, RuntimeAuth: *auth, WarmTTL: *ttl, MaxConcurrent: *max})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return supervisor.Serve(ctx, m, *listen)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func supervisorAuthFromEnv() string {
	if value := os.Getenv("HUB_SUPERVISOR_AUTH"); value != "" {
		return value
	}
	return os.Getenv("HUB_RUNTIME_AUTH")
}
