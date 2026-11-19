package main

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"os"
)

func main() {
	if err := toolhub.Main(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hub-toolhub:", err)
		os.Exit(1)
	}
}
