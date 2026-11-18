package main

import (
	"context"
	"testing"
)

// Dispatch-level usage gates: every op rejects malformed invocations before
// touching docker, the store or the network.
func TestRunDispatchUsage(t *testing.T) {
	for _, args := range [][]string{
		{"hosting-init", "extra"},
		{"hosting"},
		{"hosting", "--config", "no-such-deployment.json"},
		{"relay", "--target", "http://x"},
		{"status", "extra"},
		{"image", "--dir"},
		{"image", "--env", "staging"},
		{"org"},
		{"org", "sync-members"},
		{"profile"},
		{"profile", "grants"},
		{"profile", "diff"},
		{"profile", "bogus"},
		{"space"},
		{"space", "bogus"},
		{"replay"},
		{"replay", "bogus"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

// With both sections skipped, `status` renders an empty document — the same
// contract `hubctl status` gives an undeployed host.
func TestRunStatusSkipped(t *testing.T) {
	captureOutput(t, func() error {
		return run(context.Background(), []string{"status", "--dir", t.TempDir(), "--supervisor-url", "", "--gateway-url", ""})
	})
	captureOutput(t, func() error {
		return run(context.Background(), []string{"status", "--dir", t.TempDir(), "--supervisor-url", "", "--gateway-url", "", "--json"})
	})
}

// Context/memory/skill/routine subcommands: usage gates plus the trivially
// reachable success paths on an empty space dir.
func TestRunContextMemorySubcommands(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"context"},
		{"context", "backup"},        // requires --out
		{"context", "restore"},       // requires --in
		{"context", "restore-drill"}, // no scheduled backups
		{"context", "bogus"},
		{"memory"},
		{"memory", "search"}, // requires --reindex
		{"memory", "delete"}, // requires --name
		{"memory", "bogus"},
		{"skill"},
		{"routine"},
		{"routine", "list"}, // requires toolhub-url + token
	} {
		full := append(append([]string{}, args...), "--dir", dir)
		if err := run(context.Background(), full); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
	// stop-runtime/delete-runtime are pure advisories — no filesystem work.
	for _, sub := range []string{"stop-runtime", "delete-runtime"} {
		captureOutput(t, func() error {
			return run(context.Background(), []string{"context", sub, "--dir", dir})
		})
	}
}
