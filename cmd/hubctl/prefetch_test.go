package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// connectorPrefetch must pull pinned catalog images, evict only superseded
// digests of catalog-owned repositories inside the 8 GB budget, and never
// touch foreign images.
func TestConnectorPrefetchPullsWantedAndKeepsForeign(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		switch args[0] {
		case "manifest":
			return []byte(`{"config":{"size":100},"layers":[{"size":400}]}`), nil
		case "images":
			return []byte(strings.Join([]string{
				"ghcr.io/acme/foreign|latest|sha256:" + strings.Repeat("f", 64) + "|7GB",
				"localhost:5543/playwright-mcp|old|sha256:" + strings.Repeat("e", 64) + "|6GB",
			}, "\n")), nil
		case "pull", "rmi":
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected docker %v", args)
	}
	if err := connectorPrefetch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	var pulls, evicts int
	for _, call := range calls {
		if call[0] == "pull" {
			pulls++
			if !strings.Contains(call[1], "@sha256:") {
				t.Fatalf("prefetch pulled a mutable reference: %v", call)
			}
		}
		if call[0] == "rmi" {
			evicts++
			if strings.Contains(call[1], "foreign") {
				t.Fatalf("prefetch evicted a foreign image: %v", call)
			}
		}
	}
	if pulls == 0 {
		t.Fatal("prefetch pulled nothing despite an uncached digest")
	}
	if evicts == 0 {
		t.Fatal("prefetch left a superseded owned image despite cache pressure")
	}
}

// `hubctl connector prefetch` must reach the prefetch flow instead of the
// tool-call fallthrough.
func TestRunConnectorPrefetchDispatches(t *testing.T) {
	old := prefetchRunner
	defer func() { prefetchRunner = old }()
	var called bool
	prefetchRunner = func(_ context.Context, args ...string) ([]byte, error) {
		called = true
		if args[0] == "manifest" {
			return []byte(`{"config":{"size":0},"layers":[]}`), nil
		}
		return []byte(""), nil
	}
	if err := runConnector(context.Background(), []string{"prefetch"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("connector prefetch never reached docker")
	}
}

func TestParseDockerSize(t *testing.T) {
	for input, want := range map[string]int64{
		"1.2GB":  1288490188,
		"512MB":  536870912,
		"75.5kB": 77312,
		"900B":   900,
		"bogus":  0,
	} {
		if got := parseDockerSize(input); got != want {
			t.Fatalf("parseDockerSize(%q) = %d, want %d", input, got, want)
		}
	}
}
