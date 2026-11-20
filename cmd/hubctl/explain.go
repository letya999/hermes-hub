package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/shared/locale"
)

// runExplain prints the per-leaf provenance of a space's effective spec:
// every effective field, its value and the layer that set it. The command
// talks to spawn-controller through the v1 contract — the service owns the
// attribution it recorded at activation, so the report can never drift
// from the spec the space actually runs.
func runExplain(ctx context.Context, w io.Writer, args []string, client spawnv1.Client) error {
	lang := locale.Operator()
	f := flag.NewFlagSet("explain", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	org := f.String("org", "local", "")
	owner := f.String("owner", "", "")
	if err := f.Parse(args); err != nil || f.NArg() != 1 {
		return errors.New(locale.T(lang, "hubctl.explain-usage"))
	}
	space := f.Arg(0)
	if *owner == "" {
		*owner = space
	}
	report, err := client.Explain(ctx, spawnv1.Tenant{Org: *org, Owner: *owner}, space)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\n", locale.T(lang, "hubctl.explain-header", report.Space, report.Digest))
	for _, field := range report.Fields {
		value, err := json.Marshal(field.Value)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s.%s = %s [%s]\n", field.Doc, field.Path, value, field.Layer)
	}
	return nil
}

// explainClient resolves the spawn-controller contract client the way the
// supervisor URL resolves: an explicit HUB_SPAWN_CONTROLLER_URL always
// wins (process mode exports it); unset means the compose stack's
// loopback publication. The shared service token is HUB_SERVICE_TOKEN,
// then the supervisor auth chain — env first, then the infra space's
// supervisor.auth, the same credential the compose service consumes.
func explainClient(args []string) (spawnv1.Client, error) {
	explicit := os.Getenv("HUB_SPAWN_CONTROLLER_URL")
	base := explicit
	if base == "" {
		base = "http://127.0.0.1:8877"
	}
	token := os.Getenv("HUB_SERVICE_TOKEN")
	if token == "" {
		token = supervisorAuthFromEnv()
	}
	if token == "" {
		dir := filepath.Join("spaces", explainSpaceName(args))
		if auth, err := stack.ReadSecrets(filepath.Join(stack.InfraSpaceDir(dir), "supervisor.auth")); err == nil {
			token = strings.TrimSpace(auth["HUB_SUPERVISOR_AUTH"])
		}
	}
	if token == "" && explicit == "" {
		return nil, errors.New(locale.T(locale.Operator(), "hubctl.explain-no-controller"))
	}
	return spawnv1.NewHTTPClient(base, token, nil), nil
}

// explainSpaceName extracts the positional space argument so the auth
// lookup knows which spaces root to search — flag values never matter.
func explainSpaceName(args []string) string {
	f := flag.NewFlagSet("explain", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	_ = f.String("org", "local", "")
	_ = f.String("owner", "", "")
	_ = f.Parse(args)
	if f.NArg() != 1 {
		return ""
	}
	return f.Arg(0)
}
