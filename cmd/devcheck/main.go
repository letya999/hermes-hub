package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/devcheck"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "devcheck:", err)
		os.Exit(1)
	}
}

var coverAll = devcheck.CoverAll

func run(args []string) error {
	var err error
	switch {
	case len(args) == 1 && args[0] == "docs":
		err = devcheck.Project(".")
	case len(args) == 1 && args[0] == "format":
		err = devcheck.Format(".")
	case len(args) >= 3 && len(args) <= 4 && args[0] == "coverage":
		minimum, pkgMin, parseErr := parseMinimums(args)
		if parseErr != nil {
			err = parseErr
		} else if actual, checkErr := devcheck.CoverageGate(args[1], minimum, pkgMin); checkErr != nil {
			err = checkErr
		} else {
			fmt.Printf("Go statement coverage: %.2f%%\n", actual)
		}
	case len(args) >= 3 && len(args) <= 4 && args[0] == "covergo":
		minimum, pkgMin, parseErr := parseMinimums(args)
		if parseErr != nil {
			err = parseErr
		} else {
			actual, checkErr := coverAll(args[1], minimum, pkgMin,
				func() ([]string, error) {
					listed, listErr := exec.Command("go", "list", "./...").Output()
					if listErr != nil {
						return nil, listErr
					}
					return strings.Fields(string(listed)), nil
				},
				func(pkg, out string) error {
					test := exec.Command("go", "test", "-race", "-shuffle=on", "-count=1", "-coverpkg=./...", "-coverprofile="+out, pkg)
					test.Stdout = os.Stdout
					test.Stderr = os.Stderr
					return test.Run()
				})
			if checkErr != nil {
				err = checkErr
			} else {
				fmt.Printf("Go statement coverage: %.2f%%\n", actual)
			}
		}
	case len(args) == 2 && args[0] == "docker-smoke":
		err = devcheck.DockerSmoke(context.Background(), args[1])
	case len(args) == 3 && args[0] == "docker-build":
		err = devcheck.DockerBuild(context.Background(), args[1], args[2])
	case len(args) == 3 && args[0] == "docker-pull":
		err = devcheck.DockerPull(context.Background(), args[1], args[2])
	case len(args) == 2 && args[0] == "docker-publish":
		err = devcheck.DockerPublish(context.Background(), args[1])
	case len(args) == 1 && args[0] == "docker-clean":
		err = devcheck.DockerClean(context.Background(), ".", false)
	case len(args) == 2 && args[0] == "docker-clean" && args[1] == "--deep":
		err = devcheck.DockerClean(context.Background(), ".", true)
	case len(args) == 2 && args[0] == "hermes-contract":
		err = devcheck.HermesContract(context.Background(), args[1])
	case len(args) == 2 && args[0] == "hermes-native-contract":
		err = devcheck.HermesNativeContract(context.Background(), args[1])
	case len(args) == 2 && args[0] == "supervisor-contract":
		err = devcheck.SupervisorContract(context.Background(), args[1])
	case len(args) == 2 && args[0] == "tts-cache-contract":
		err = devcheck.TTSCachedModelCanary(context.Background(), args[1])
	case len(args) == 2 && args[0] == "hermes-capability-contract":
		err = devcheck.HermesCapabilityContract(context.Background(), args[1])
	case len(args) == 2 && args[0] == "managed-network-canary":
		err = devcheck.ManagedNetworkCanary(context.Background(), args[1])
	case len(args) == 2 && args[0] == "managed-supervisor-canary":
		err = devcheck.ManagedSupervisorCanary(context.Background(), args[1])
	case len(args) == 2 && args[0] == "scratch-workload-canary":
		err = devcheck.ScratchWorkloadCanary(context.Background(), args[1])
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
		err = fmt.Errorf("usage: devcheck docs|format|coverage FILE MINIMUM [PACKAGE-MINIMUM]|covergo FILE MINIMUM [PACKAGE-MINIMUM]|docker-build IMAGE TARGET|docker-pull IMAGE TARGET|docker-publish TARGET|docker-clean [--deep]|docker-smoke IMAGE|hermes-contract IMAGE|hermes-native-contract IMAGE|supervisor-contract IMAGE|tts-cache-contract IMAGE|hermes-capability-contract IMAGE|managed-network-canary IMAGE|managed-supervisor-canary IMAGE|scratch-workload-canary IMAGE|gateway-lifecycle IMAGE|toolhub-contract|toolhub-gateway-contract|toolhub-hermes-contract|mcp-bench URL TOOL CALLS")
	}
	return err
}

func parseMinimums(args []string) (float64, float64, error) {
	minimum, err := strconv.ParseFloat(args[2], 64)
	if err != nil {
		return 0, 0, err
	}
	pkgMin := 0.0
	if len(args) == 4 {
		pkgMin, err = strconv.ParseFloat(args[3], 64)
	}
	return minimum, pkgMin, err
}
