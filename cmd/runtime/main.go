package main

import (
	"fmt"
	"log"
	"os"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := hubruntime.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "runtime:", err)
		os.Exit(1)
	}
}
