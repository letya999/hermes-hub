package main

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	"os"
)

func main() {
	if err := communication.Main(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "communication:", err)
		os.Exit(1)
	}
}
