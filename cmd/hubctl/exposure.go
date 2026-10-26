package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/exposure"
)

// runExposure implements `hubctl exposure enrol`: it materializes the sish
// keypair for the reviewed per-owner config and prints the operator-facing
// enrolment steps. The private half is never printed.
func runExposure(_ context.Context, args []string) error {
	f := flag.NewFlagSet("exposure", flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	cfgPath := filepath.Join(*dir, "connections", "exposure", "config.yaml")
	cfg, err := exposure.Load(cfgPath)
	if err != nil {
		return err
	}
	res, err := cfg.Enrol(filepath.Dir(cfgPath))
	if err != nil {
		return err
	}
	fmt.Printf("sish enrolment for %s\n\n", cfgPath)
	fmt.Printf("public key (add to %s's authorized_keys on %s):\n%s\n", cfg.Relay.User, cfg.Relay.Address(), res.PublicLine)
	fmt.Printf("DNS: point *.%s.%s at %s\n", cfg.Zone, cfg.Relay.Domain, cfg.Relay.Host)
	return nil
}
