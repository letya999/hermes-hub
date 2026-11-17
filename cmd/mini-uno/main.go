// Command mini-uno is a local Uno-compatible Firecracker control plane for
// development and live qualification (WSL2 /dev/kvm or bare metal).
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/letya999/hermes-hub/internal/firecracker"
)

func main() {
	cfg := firecracker.Config{
		Listen:    env("MINI_UNO_LISTEN", "127.0.0.1:8320"),
		Token:     os.Getenv("MINI_UNO_TOKEN"),
		Kernel:    os.Getenv("MINI_UNO_KERNEL"),
		RootFS:    os.Getenv("MINI_UNO_ROOTFS"),
		WorkDir:   env("MINI_UNO_WORKDIR", "/tmp/mini-uno"),
		FCBin:     env("MINI_UNO_FCBIN", "firecracker"),
		VirtioFSD: env("MINI_UNO_VIRTIOFSD", "virtiofsd"),
	}
	m, err := firecracker.NewManager(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mini-uno:", err)
		os.Exit(1)
	}
	log.Printf("mini-uno: listening on %s workdir=%s", cfg.Listen, cfg.WorkDir)
	if err := http.ListenAndServe(cfg.Listen, m); err != nil {
		fmt.Fprintln(os.Stderr, "mini-uno:", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
