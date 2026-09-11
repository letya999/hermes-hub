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
	case len(args) == 2 && args[0] == "hermes-contract":
		err = devcheck.HermesContract(context.Background(), args[1])
	default:
		err = fmt.Errorf("usage: devcheck docs|format|coverage FILE MINIMUM|docker-smoke IMAGE|hermes-contract IMAGE")
	}
	return err
}
