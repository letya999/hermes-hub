package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/letya999/credential-broker/app"
)

const version = "0.1.0"

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: credential-broker init --dir PATH | serve --config PATH | check-config --config PATH | version")
	}
	switch args[0] {
	case "version":
		_, e := fmt.Fprintln(out, version)
		return e
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		fs.SetOutput(out)
		dir := fs.String("dir", "", "absolute development installation directory")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if *dir == "" || fs.NArg() != 0 {
			return fmt.Errorf("--dir is required")
		}
		path, e := filepath.Abs(*dir)
		if e != nil {
			return e
		}
		if e = app.Init(path); e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, "Development installation created. Signing private keys belong to the trusted Hub, never to an MCP container.")
		return e
	case "serve", "check-config":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		fs.SetOutput(out)
		path := fs.String("config", "", "absolute configuration path")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		if *path == "" || fs.NArg() != 0 {
			return fmt.Errorf("--config is required")
		}
		cfg, e := app.ReadConfig(*path)
		if e != nil {
			return e
		}
		if args[0] == "check-config" {
			_, e = fmt.Fprintln(out, "Configuration syntax is valid. Adapter credentials, TLS and contract readiness are verified at service startup.")
			return e
		}
		if os.Getenv("BROKER_DIRECT_FORM") == "1" {
			cfg.DirectForm = true
		}
		svc, e := app.Build(cfg)
		if e != nil {
			return e
		}
		ln, e := net.Listen("tcp", cfg.Listen)
		if e != nil {
			_ = svc.Broker.Close()
			return e
		}
		_, _ = fmt.Fprintln(out, "Credential Broker listening. Request bodies and URLs are not logged.")
		return svc.Run(ctx, ln)
	default:
		return fmt.Errorf("unknown command")
	}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stderr); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
