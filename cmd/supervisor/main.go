package main

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"os"
)

func main() {
	if err := supervisor.Main(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hub-supervisor:", err)
		os.Exit(1)
	}
}
