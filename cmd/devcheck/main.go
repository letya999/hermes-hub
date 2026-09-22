package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/letya999/hermes-hub/internal/devcheck"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "devcheck:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var err error
	switch {
	case len(args) == 1 && args[0] == "docs":
		err = devcheck.Project(".")
	case len(args) == 1 && args[0] == "format":
		err = devcheck.Format(".")
	case len(args) == 3 && args[0] == "coverage":
		minimum, parseErr := strconv.ParseFloat(args[2], 64)
		if parseErr != nil {
			err = parseErr
		} else if actual, checkErr := devcheck.Coverage(args[1], minimum); checkErr != nil {
			err = checkErr
		} else {
			fmt.Printf("Go statement coverage: %.2f%%\n", actual)
		}
	case len(args) == 2 && args[0] == "docker-smoke":
		err = devcheck.DockerSmoke(context.Background(), args[1])
	case len(args) == 3 && args[0] == "docker-build":
		err = devcheck.DockerBuild(context.Background(), args[1], args[2])
	case len(args) == 1 && args[0] == "docker-clean":
		err = devcheck.DockerClean(context.Background(), ".", false)
	case len(args) == 2 && args[0] == "docker-clean" && args[1] == "--deep":
		err = devcheck.DockerClean(context.Background(), ".", true)
	case len(args) == 2 && args[0] == "hermes-contract":
		err = devcheck.HermesContract(context.Background(), args[1])
	case len(args) == 2 && args[0] == "gateway-lifecycle":
		err = devcheck.GatewayLifecycle(context.Background(), args[1])
	case len(args) == 1 && args[0] == "toolhub-contract":
		err = devcheck.ToolHubContract(context.Background())
	case len(args) == 1 && args[0] == "toolhub-gateway-contract":
		err = devcheck.ToolHubGatewayContract(context.Background())
	case len(args) == 1 && args[0] == "toolhub-hermes-contract":
		err = devcheck.ToolHubHermesContract(context.Background())
	case len(args) == 4 && args[0] == "mcp-bench":
		calls, parseErr := strconv.Atoi(args[3])
		if parseErr != nil {
			err = parseErr
		} else {
			err = devcheck.MCPBench(context.Background(), args[1], args[2], calls, os.Stdout)
		}
	default:
		err = fmt.Errorf("usage: devcheck docs|format|coverage FILE MINIMUM|docker-build IMAGE TARGET|docker-clean [--deep]|docker-smoke IMAGE|hermes-contract IMAGE|gateway-lifecycle IMAGE|toolhub-contract|toolhub-gateway-contract|toolhub-hermes-contract|mcp-bench URL TOOL CALLS")
	}
	return err
}
