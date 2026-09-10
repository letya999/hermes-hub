package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/companion"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hubctl:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Println("hubctl 0.2.0: init | org-init | render | doctor | catalog | build | up | down | logs | chat | telegram-login | meet-auth | tools | companion\nFlags: --dir spaces/me --root . --user me --org acme --env prod\nSee README.md for account setup and private VPS access.")
		return nil
	}
	op := args[0]
	f := flag.NewFlagSet(op, flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	root := f.String("root", ".", "source root")
	profile := f.String("user", "me", "person identifier")
	organization := f.String("org", "", "organization identifier")
	organizationRoot := f.String("organization", "", "mounted organization docs root for tools")
	organizationDir := f.String("org-dir", "", "organization configuration directory")
	environment := f.String("env", "prod", "dev or prod")
	workspace := f.String("workspace", "/workspace", "workspace root")
	archive := f.String("archive", "/archive", "read-only archive root")
	config := f.String("config", "companion.yaml", "native bridge config")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 && op != "exec" {
		return fmt.Errorf("unexpected arguments")
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *profile)
	}
	switch op {
	case "init":
		if *organization != "" {
			return stack.InitEnvironmentWithOrganization(*dir, *profile, *environment, *organization)
		}
		return stack.InitEnvironment(*dir, *profile, *environment)
	case "org-init":
		if *organization == "" {
			return fmt.Errorf("org-init requires --org")
		}
		if *organizationDir == "" {
			*organizationDir = filepath.Join("organizations", *organization)
		}
		return stack.InitOrganization(*organizationDir, *organization, *profile)
	case "catalog":
		return json.NewEncoder(os.Stdout).Encode(stack.Features)
	case "companion":
		return companion.Run(ctx, *config)
	case "tools":
		t, err := agenttools.Open(*workspace, *archive, *organizationRoot)
		if err != nil {
			return err
		}
		defer t.Close()
		return t.Server().Run(ctx, &mcp.StdioTransport{})
	case "render":
		return stack.RenderEnvironment(*dir, *root, *environment)
	}
	if !slices.Contains([]string{"doctor", "build", "up", "down", "logs", "chat", "telegram-login", "meet-auth", "exec"}, op) {
		return fmt.Errorf("unknown command %q", op)
	}
	if op == "doctor" || op == "up" {
		s, err := stack.ReadEnvironment(*dir, *environment)
		if err != nil {
			return err
		}
		secrets, err := stack.ReadSecrets(filepath.Join(*dir, "secrets."+*environment+".env"))
		if err != nil {
			return err
		}
		orgSecrets, err := stack.ReadOrganizationSecrets(s, *environment)
		if err != nil {
			return err
		}
		issues := stack.DoctorScope(s, secrets, orgSecrets)
		if len(issues) > 0 {
			return fmt.Errorf("configuration incomplete:\n- %s", strings.Join(issues, "\n- "))
		}
		if op == "doctor" {
			fmt.Println("Configuration valid. OAuth sessions, provider permissions and Docker runtime require live verification.")
			return nil
		}
	}
	if op == "build" || op == "up" {
		if err := stack.RenderEnvironment(*dir, *root, *environment); err != nil {
			return err
		}
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	prefix := []string{"compose", "-f", filepath.Join(abs, "compose."+*environment+".yaml")}
	docker := func(a ...string) error {
		cmd := exec.CommandContext(ctx, "docker", append(append([]string{}, prefix...), a...)...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	if op == "build" || op == "up" {
		if err = docker("build"); err != nil {
			return err
		}
		if op == "build" {
			return nil
		}
		return docker("up", "-d", "--wait", "--wait-timeout", "180", "--remove-orphans")
	}
	switch op {
	case "down":
		return docker("down", "--remove-orphans")
	case "logs":
		return docker("logs", "--tail", "100", "-f")
	case "exec":
		if f.NArg() == 0 {
			return fmt.Errorf("exec requires -- command arguments")
		}
		return docker(append([]string{"exec", "hermes-runtime"}, f.Args()...)...)
	case "chat":
		s, err := stack.ReadEnvironment(abs, *environment)
		if err != nil {
			return err
		}
		if s.Has("telegram") {
			return fmt.Errorf("gateway owns this home; use Telegram or a separate dev space for CLI chat")
		}
		return docker("exec", "hermes-runtime", "hermes", "chat")
	case "telegram-login":
		return docker("run", "--rm", "--no-deps", "--entrypoint", "/opt/telegram/.venv/bin/python", "hermes-runtime", "/opt/telegram/session_string_generator.py", "--phone")
	case "meet-auth":
		return docker("exec", "hermes-runtime", "hermes", "meet", "auth")
	}
	return nil
}
