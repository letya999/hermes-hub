package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

type jobResolveOptions struct {
	Dir         string
	Environment string
	JobID       string
	Status      string
	GatewayURL  string
	GatewayAuth string
	Client      *http.Client
}

// runJob owns operator verbs on one durable gateway job. `resolve` settles an
// uncertain job the supervisor can no longer settle, which frees the
// conversation lane its admitted run still holds.
func runJob(ctx context.Context, args []string) error {
	usage := locale.T(locale.Operator(), "hubctl.job-resolve-usage")
	if len(args) == 0 || args[0] != "resolve" {
		return fmt.Errorf("%s", usage)
	}
	f := flag.NewFlagSet("job resolve", flag.ContinueOnError)
	opts := jobResolveOptions{GatewayURL: "http://127.0.0.1:8081"}
	f.StringVar(&opts.Dir, "dir", "", "space directory")
	f.StringVar(&opts.Environment, "env", "dev", "deployment env")
	f.StringVar(&opts.JobID, "job", "", "uncertain job id")
	f.StringVar(&opts.Status, "status", "interrupted", "terminal status: interrupted, failed or cancelled")
	f.StringVar(&opts.GatewayURL, "gateway-url", opts.GatewayURL, "communication gateway base URL")
	f.StringVar(&opts.GatewayAuth, "gateway-auth", "", "communication service-control bearer")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if opts.Dir == "" || opts.JobID == "" || f.NArg() != 0 {
		return fmt.Errorf("%s", usage)
	}
	return runJobResolve(ctx, os.Stdout, opts)
}

func runJobResolve(ctx context.Context, out io.Writer, opts jobResolveOptions) error {
	lang := locale.Operator()
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return err
	}
	if opts.GatewayAuth == "" {
		opts.GatewayAuth = resolveGatewayToken(dir, opts.Environment, resolveStatusToken(dir))
	}
	if opts.GatewayAuth == "" {
		return fmt.Errorf("%s", locale.T(lang, "hubctl.job-resolve-auth"))
	}
	body, err := json.Marshal(map[string]string{"job_id": opts.JobID, "status": opts.Status})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.GatewayURL+"/v1/admin/jobs/resolve", bytes.NewReader(body)) // #nosec G704 -- operator-configured control-plane origin
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+opts.GatewayAuth)
	req.Header.Set("Content-Type", "application/json")
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", locale.T(lang, "hubctl.job-resolve-failed", opts.JobID, resp.StatusCode))
	}
	_, err = fmt.Fprintln(out, locale.T(lang, "hubctl.job-resolved", opts.JobID, opts.Status))
	return err
}
