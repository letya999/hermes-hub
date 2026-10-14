// e2e runs the acceptance registry without implicitly logging into any account.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/e2e"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: e2e list|mock|mock-server|telegram [--select E2E-001,E2E-002] [--report PRIVATE_PATH] [--principal ID] [--interactive]")
	}
	f := flag.NewFlagSet("e2e", flag.ContinueOnError)
	selectIDs := f.String("select", "", "comma-separated case IDs within selected scope")
	scope := f.String("scope", "core", "core excludes account-dependent connectors; all lists the full inventory")
	reportPath := f.String("report", "", "private local report path, outside Git")
	interactive := f.Bool("interactive", false, "guided real Telegram observations")
	principal := f.String("principal", "", "explicit gateway principal; server mode runs inside communication-hub")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	cases, err := e2e.Registry()
	if err != nil {
		return err
	}
	if *scope != "core" && *scope != "all" {
		return fmt.Errorf("scope must be core or all")
	}
	if *scope == "core" {
		cases = e2e.CoreCases(cases)
	}
	if *selectIDs != "" {
		wanted := map[string]bool{}
		for _, id := range strings.Split(*selectIDs, ",") {
			wanted[id] = true
		}
		selected := []e2e.Case{}
		for _, c := range cases {
			if wanted[c.ID] {
				selected = append(selected, c)
				delete(wanted, c.ID)
			}
		}
		if len(wanted) != 0 {
			return fmt.Errorf("unknown case selection")
		}
		cases = selected
	}
	if args[0] == "list" {
		return json.NewEncoder(os.Stdout).Encode(cases)
	}
	var report e2e.Report
	switch args[0] {
	case "mock":
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		report, err = e2e.Mock(ctx, cases, e2e.Command, os.Stdout)
	case "telegram":
		if *interactive {
			report = e2e.Telegram(cases, os.Stdin, os.Stdout, true)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
			defer cancel()
			report, err = e2e.Server(ctx, cases, *principal, os.Stdout)
		}
	case "mock-server":
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		report, err = e2e.MockServer(ctx, cases, *principal, os.Stdout)
	default:
		return fmt.Errorf("unknown mode %q", args[0])
	}
	if *reportPath != "" {
		data, encodeErr := json.MarshalIndent(report, "", "  ")
		if encodeErr != nil {
			return encodeErr
		}
		if writeErr := os.WriteFile(*reportPath, append(data, '\n'), 0600); writeErr != nil {
			return writeErr
		}
	}
	if err != nil {
		return err
	}
	if !e2e.Complete(report) {
		return fmt.Errorf("suite incomplete: cases require more evidence; mocks and partial checks are not live acceptance")
	}
	return nil
}
