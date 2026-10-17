package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/diagnostics"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(os.Args) > 1 && os.Args[1] == "--script" {
		if err := runScriptHelper(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "script failed")
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	config, err := communication.ConfigFromEnv()
	if err == nil {
		var gateway *communication.Gateway
		gateway, err = communication.New(config)
		if err == nil {
			if addr := strings.TrimSpace(config.ListenAddr); addr != "" {
				server := &http.Server{Addr: addr, Handler: diagnostics.HTTP("communication", gateway.Handler()), ReadHeaderTimeout: 10 * time.Second}
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

func runScriptHelper(args []string, out io.Writer) error {
	if len(args) != 2 && len(args) != 4 {
		return fmt.Errorf("invalid script arguments")
	}
	source, err := base64.StdEncoding.DecodeString(args[0])
	if err != nil {
		return err
	}
	due, err := time.Parse(time.RFC3339Nano, args[1])
	if err != nil {
		return err
	}
	var input, watchRoot string
	if len(args) == 4 {
		raw, err := base64.StdEncoding.DecodeString(args[2])
		if err != nil {
			return err
		}
		input = string(raw)
		if args[3] != "-" {
			watchRoot = args[3]
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watchdog := time.AfterFunc(5*time.Second, func() { os.Exit(1) })
	defer watchdog.Stop()
	emitted, err := communication.RunScriptProbe(ctx, string(source), due, input, watchRoot)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(emitted)
}
