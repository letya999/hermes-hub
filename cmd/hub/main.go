package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hub:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	usage := func() error { return errors.New(locale.T(locale.Fallback(), "hub.usage")) }
	if len(args) < 2 || args[0] != "serve" {
		return usage()
	}
	zone, rest := "", args[2:]
	if args[1] == "--zone" && len(args) >= 3 {
		zone, rest = args[2], args[3:]
	} else if value, ok := strings.CutPrefix(args[1], "--zone="); ok {
		zone = value
	} else {
		return usage()
	}
	switch zone {
	case "control":
		return supervisor.Main(rest)
	case "agent":
		return toolhub.Main(rest)
	case "edge":
		return communication.Main(rest)
	default:
		return usage()
	}
}
