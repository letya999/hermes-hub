package communication

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/shared/locale"
)

// Main runs the edge zone or its bounded script subprocess helper.
func Main(args []string) error {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(args) > 0 && args[0] == "--script" {
		if err := runScriptHelper(args[1:], os.Stdout); err != nil {
			return errors.New(locale.T(locale.Fallback(), "hub.script-failed"))
		}
		return nil
	}
	if len(args) != 0 {
		return errors.New(locale.T(locale.Fallback(), "hub.edge-usage"))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	config, err := ConfigFromEnv()
	if err == nil {
		var gateway *Gateway
		gateway, err = New(config)
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
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func runScriptHelper(args []string, out io.Writer) error {
	// argv: b64(source) dueAt [b64(input) watchRoot [b64(need) xN]] — needs are
	// hub-prefetched read-tool results capped at four by the supervisor.
	if len(args) != 2 && (len(args) < 4 || len(args) > 8) {
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
	var needs []string
	if len(args) >= 4 {
		raw, err := base64.StdEncoding.DecodeString(args[2])
		if err != nil {
			return err
		}
		input = string(raw)
		if args[3] != "-" {
			watchRoot = args[3]
		}
		for _, encoded := range args[4:] {
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return err
			}
			needs = append(needs, string(raw))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watchdog := time.AfterFunc(5*time.Second, func() { os.Exit(1) })
	defer watchdog.Stop()
	emitted, err := RunScriptProbe(ctx, string(source), due, input, watchRoot, needs)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(emitted)
}
