// testexec retries Windows' transient refusal to start a freshly linked Go
// test binary. It does not retry a test that ran and failed.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	os.Exit(run(func() error {
		cmd := exec.Command(os.Args[1], os.Args[2:]...) // #nosec G702 -- Go supplies executable and argv through -exec/-toolexec; no shell is used
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}, time.Sleep))
}

func run(launch func() error, pause func(time.Duration)) int {
	for attempt := 0; attempt < 5; attempt++ {
		err := launch()
		if err == nil {
			return 0
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if !os.IsPermission(err) || attempt == 4 {
			fmt.Fprintln(os.Stderr, "testexec:", err)
			return 1
		}
		pause(200 * time.Millisecond)
	}
	return 1
}
