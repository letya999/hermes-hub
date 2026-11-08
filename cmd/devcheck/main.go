package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/letya999/hermes-hub/internal/devcheck"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "devcheck:", err)
		os.Exit(1)
	}
}

func testExecPath() (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", fmt.Errorf("devcheck: Go module root unavailable")
		}
		root = parent
	}
	return filepath.Join(root, ".work", "tmp", "devcheck-testexec.exe"), nil
}

func prepareTestExec() (string, error) {
	if runtime.GOOS != "windows" {
		return "", nil
	}
	path, err := testExecPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	build := exec.Command("go", "build", "-o", path, "./cmd/devcheck/testexec")
	build.Dir = filepath.Dir(filepath.Dir(filepath.Dir(path)))
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return "", err
	}
	return path, nil
}

// coverTest runs the whole suite in one invocation like a CI shard
// (.github/workflows/ci.yml): Go merges the -coverpkg=./... profiles natively.
// Windows limits package concurrency and retries transient executable launch
// refusals. The old
// per-package loop re-linked and ran the same instrumented binary 49 times
// serially (15-20 min locally).
var coverTest = func(profile, testExec string) *exec.Cmd {
	args := []string{"test", "-race", "-shuffle=on", "-count=1", "-timeout=10m", "-coverpkg=./...", "-coverprofile=" + profile}
	if runtime.GOOS == "windows" {
		args = append(args, "-p=2")
		if testExec != "" {
			args = append(args, "-exec="+testExec, "-toolexec="+testExec)
		}
	}
	return exec.Command("go", append(args, "./...")...)
}

func run(args []string) error {
	var err error
	switch {
	case len(args) == 1 && args[0] == "docs":
		err = devcheck.Project(".")
	case len(args) == 1 && args[0] == "catalog-emit":
		err = devcheck.CatalogEmit(".")
	case len(args) == 1 && args[0] == "format":
		err = devcheck.Format(".")
	case len(args) == 1 && args[0] == "gosec":
		for _, scan := range []struct {
			dir  string
			pkgs []string
		}{{".", []string{"./cmd/...", "./internal/..."}}, {"services/credential-broker", []string{"./..."}}} {
			argv := append([]string{"run", "github.com/securego/gosec/v2/cmd/gosec@v2.29.1-0.20261022115301-1cbfd7e6488c", "-quiet", "-exclude-generated", "-exclude=G101,G204,G301,G302,G304,G306"}, scan.pkgs...)
			cmd := exec.Command("go", argv...)
			cmd.Env = append(os.Environ(), "GOWORK=off")
			cmd.Dir, cmd.Stdout, cmd.Stderr = scan.dir, os.Stdout, os.Stderr
			if err = cmd.Run(); err != nil {
				return fmt.Errorf("gosec %s: %w", scan.dir, err)
			}
		}
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
			testExec, pathErr := prepareTestExec()
			if pathErr != nil {
				return pathErr
			}
			test := coverTest(args[1], testExec)
			test.Stdout, test.Stderr = os.Stdout, os.Stderr
			if testErr := test.Run(); testErr != nil {
				err = testErr
			} else {
				actual, checkErr := devcheck.CoverageGate(args[1], minimum, pkgMin)
				if checkErr != nil {
					err = checkErr
				} else {
					fmt.Printf("Go statement coverage: %.2f%%\n", actual)
				}
			}
		}
	case len(args) > 1 && args[0] == "test":
		testExec, pathErr := prepareTestExec()
		if pathErr != nil {
			return pathErr
		}
		flags := []string{"test", "-short", "-p=2", "-timeout=5m"}
		if testExec != "" {
			flags = append(flags, "-exec="+testExec, "-toolexec="+testExec)
		}
		test := exec.Command("go", append(flags, args[1:]...)...) // #nosec G702 -- local developer package arguments go directly to the fixed go executable, without a shell
		test.Stdout, test.Stderr = os.Stdout, os.Stderr
		err = test.Run()
	case len(args) == 2 && args[0] == "docker-smoke":
		err = devcheck.DockerSmoke(context.Background(), args[1])
	case len(args) == 3 && args[0] == "image-budget":
		err = devcheck.ImageBudget(context.Background(), args[1], args[2])
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
	case len(args) == 3 && args[0] == "m32-lifecycle":
		err = devcheck.M32Lifecycle(context.Background(), args[1], args[2])
	case len(args) == 1 && args[0] == "toolhub-contract":
		err = devcheck.ToolHubContract(context.Background())
	case len(args) == 1 && args[0] == "toolhub-gateway-contract":
		err = devcheck.ToolHubGatewayContract(context.Background())
	case len(args) == 1 && args[0] == "toolhub-hermes-contract":
		err = devcheck.ToolHubHermesContract(context.Background())
	case len(args) == 3 && args[0] == "connector-build":
		err = devcheck.ConnectorBuild(context.Background(), args[1], args[2])
	case len(args) == 2 && args[0] == "connector-ref":
		var ref string
		ref, err = devcheck.ConnectorPublishRef(context.Background(), args[1])
		if err == nil {
			fmt.Println(ref)
		}
	case len(args) == 5 && args[0] == "connector-pin":
		err = devcheck.ConnectorPin(context.Background(), args[1], args[2], args[3], args[4])
	case len(args) == 2 && args[0] == "connector-bench":
		var runs int
		runs, err = strconv.Atoi(args[1])
		if err == nil {
			err = devcheck.ConnectorBench(context.Background(), runs, os.Stdout)
		}
	case len(args) == 4 && args[0] == "mcp-bench":
		calls, parseErr := strconv.Atoi(args[3])
		if parseErr != nil {
			err = parseErr
		} else {
			err = devcheck.MCPBench(context.Background(), args[1], args[2], calls, os.Stdout)
		}
	default:
		err = fmt.Errorf("usage: devcheck docs|catalog-emit|format|gosec|coverage FILE MINIMUM [PACKAGE-MINIMUM]|covergo FILE MINIMUM [PACKAGE-MINIMUM]|docker-build IMAGE TARGET|docker-pull IMAGE TARGET|docker-publish TARGET|docker-clean [--deep]|docker-smoke IMAGE|image-budget IMAGE ROLE|hermes-contract IMAGE|hermes-native-contract IMAGE|supervisor-contract IMAGE|tts-cache-contract IMAGE|hermes-capability-contract IMAGE|managed-network-canary IMAGE|managed-supervisor-canary IMAGE|scratch-workload-canary IMAGE|gateway-lifecycle IMAGE|m32-lifecycle IMAGE ENV|toolhub-contract|toolhub-gateway-contract|toolhub-hermes-contract|mcp-bench URL TOOL CALLS|connector-build ID OUTDIR|connector-ref ID|connector-pin ID PINFILE REF CATALOG|connector-bench RUNS")
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
