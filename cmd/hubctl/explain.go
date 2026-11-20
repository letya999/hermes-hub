package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
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

// explainClient resolves the spawn-controller contract client from the
// environment the launcher exports: HUB_SPAWN_CONTROLLER_URL plus the
// shared HUB_SERVICE_TOKEN.
func explainClient() (spawnv1.Client, error) {
	base := os.Getenv("HUB_SPAWN_CONTROLLER_URL")
	if base == "" {
		return nil, errors.New(locale.T(locale.Operator(), "hubctl.explain-no-controller"))
	}
	return spawnv1.NewHTTPClient(base, os.Getenv("HUB_SERVICE_TOKEN"), nil), nil
}
