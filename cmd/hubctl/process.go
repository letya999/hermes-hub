package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/shared/locale"
	hublog "github.com/letya999/hermes-hub/shared/log"
)

func runProcesses(ctx context.Context, args []string) error {
	lang := locale.Operator()
	f := flag.NewFlagSet("run", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	mode := f.String("mode", "process", "")
	profile := f.String("profile", "minimal", "")
	trusted := f.Bool("trusted", os.Getenv("HUB_TRUSTED") == "true", "")
	bin := f.String("bin", "./bin", "")
	data := f.String("data", "./data", "")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *mode != "process" || (*profile != "minimal" && *profile != "family") {
		return errors.New(locale.T(lang, "hubctl.run-usage"))
	}
	if !*trusted {
		return errors.New(locale.T(lang, "hubctl.run-untrusted"))
	}
	return launchProcesses(ctx, profileServices(*profile), *bin, *data)
}

func serviceURLEnv(name string) string {
	return "HUB_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_URL"
}

func processEnv(svc ServiceDescriptor, services []ServiceDescriptor, data string) []string {
	var env []string
	// OS bootstrap only. Do not inherit the operator's provider credentials.
	for _, name := range append([]string{"PATH", "SYSTEMROOT", "TEMP", "TMP", "HUB_LOG_LEVEL"}, svc.Env...) {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	env = append(env, "HUB_TRUSTED=true", "HUB_SERVICE="+svc.Name,
		"HUB_LISTEN=127.0.0.1:"+strconv.Itoa(svc.Port),
		"HUB_DATABASE_URL=sqlite://"+filepath.ToSlash(filepath.Join(data, svc.DataDir)))
	for _, edge := range svc.Edges {
		for _, dependency := range services {
			if dependency.Name == edge {
				env = append(env, serviceURLEnv(edge)+"=http://127.0.0.1:"+strconv.Itoa(dependency.Port))
			}
		}
	}
	return env
}

func launchProcesses(ctx context.Context, services []ServiceDescriptor, bin, data string) error {
	lang := locale.Operator()
	paths := make([]string, len(services))
	for i, svc := range services {
		name := svc.Binary
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path, err := filepath.Abs(filepath.Join(bin, name))
		if err != nil {
			return errors.New(locale.T(lang, "hubctl.run-missing", svc.Name))
		}
		if paths[i], err = exec.LookPath(path); err != nil {
			return errors.New(locale.T(lang, "hubctl.run-missing", svc.Name))
		}
	}
	data, err := filepath.Abs(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type exit struct{ service string }
	exits := make(chan exit, len(services))
	started, waited := 0, 0
	defer func() {
		cancel()
		for waited < started {
			<-exits
			waited++
		}
	}()
	logger := hublog.New(os.Stdout, "hubctl")
	for i, svc := range services {
		cmd := exec.CommandContext(ctx, paths[i]) // #nosec G204 -- operator-selected local binary, no shell or arguments
		cmd.Env = processEnv(svc, services, data)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return errors.New(locale.T(lang, "hubctl.run-start", svc.Name))
		}
		started++
		go func(name string) {
			_ = cmd.Wait()
			exits <- exit{name}
		}(svc.Name)
		logger.Info("process.started", "service_name", svc.Name)
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 30*time.Second)
	defer readyCancel()
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, svc := range services {
		for {
			req, _ := http.NewRequestWithContext(readyCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/healthz", svc.Port), nil)
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			select {
			case <-readyCtx.Done():
				if ctx.Err() != nil {
					return nil
				}
				return errors.New(locale.T(lang, "hubctl.run-health", svc.Name))
			case done := <-exits:
				waited++
				return errors.New(locale.T(lang, "hubctl.run-exit", done.service))
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case done := <-exits:
		waited++
		return errors.New(locale.T(lang, "hubctl.run-exit", done.service))
	}
}
