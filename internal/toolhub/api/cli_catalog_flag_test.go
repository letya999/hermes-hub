package api

import (
	"slices"
	"testing"
)

// rg is case-sensitive: --Max-count exits with "unrecognized flag", which
// breaks every cli-rg-search call the moment the workload renders its args.
func TestCliRipgrepSourceUsesLowercaseMaxCount(t *testing.T) {
	args := CliRipgrepDefinition().Source.Args
	if slices.Contains(args, "--Max-count") || !slices.Contains(args, "--max-count") {
		t.Fatalf("ripgrep source args must use --max-count: %v", args)
	}
}
