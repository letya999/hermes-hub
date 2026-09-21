package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/companion"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
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
		fmt.Println("hubctl 0.3.0: init | org-init | migrate-spaces | migrate-toolhub | execution-audit | select-execution | render | doctor | catalog | artifact | build | up | down | logs | chat | telegram-login | meet-auth | tools | companion | supervisor | secret | grant | context | memory | skill | routine\nFlags: --dir spaces/me --root . --user me --org acme --env prod\nConnector controllers: local-controller (fixed Telegram) or generic-controller (trusted artifact)\nSee README.md for account setup and private VPS access.")
		return nil
	}
	op := args[0]
	if op == "connector" {
		return runConnector(ctx, args[1:])
	}
	if op == "artifact" {
		return runArtifact(ctx, args[1:])
	}
	if op == "relay" {
		f := flag.NewFlagSet("relay", flag.ContinueOnError)
		listen := f.String("listen", "127.0.0.1:8765", "private relay address")
		target := f.String("target", "", "fixed MCP target endpoint")
		tokenEnv := f.String("token-env", "", "environment variable holding the relay bearer token")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *tokenEnv == "" {
			return fmt.Errorf("relay requires --listen, --target and --token-env")
		}
		return companion.RunRelay(ctx, *listen, *target, os.Getenv(*tokenEnv))
	}
	if op == "oauth-relay" {
		f := flag.NewFlagSet("oauth-relay", flag.ContinueOnError)
		listen := f.String("listen", "0.0.0.0:3500", "private OAuth callback address")
		target := f.String("target", "", "fixed OAuth callback endpoint")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return fmt.Errorf("oauth-relay accepts no positional arguments")
		}
		return companion.RunOAuthRelay(ctx, *listen, *target)
	}
	if op == "oauth-exec-relay" {
		f := flag.NewFlagSet("oauth-exec-relay", flag.ContinueOnError)
		listen := f.String("listen", "127.0.0.1:3500", "loopback OAuth callback address")
		container := f.String("container", "", "operator-owned workload container")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return fmt.Errorf("oauth-exec-relay accepts no positional arguments")
		}
		return companion.RunOAuthExecRelay(ctx, *listen, *container)
	}
	if op == "oauth-forward" {
		return companion.OAuthForward(ctx, os.Stdin, os.Stdout)
	}
	if op == "secret" {
		return runSecret(ctx, args[1:])
	}
	if op == "grant" {
		return runGrant(ctx, args[1:])
	}
	if op == "context" {
		return runContext(ctx, args[1:])
	}
	if op == "memory" {
		return runMemory(ctx, args[1:])
	}
	if op == "skill" {
		return runSkill(ctx, args[1:])
	}
	if op == "routine" {
		return runRoutine(ctx, args[1:])
	}
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
	apply := f.Bool("apply", false, "apply migration; default is dry-run")
	stateSource := f.String("state-volume", "", "legacy runtime state directory")
	toolHubStore := f.String("toolhub-store", "", "absolute ToolHub registry path")
	workspaceSource := f.String("workspace-volume", "", "legacy workspace directory")
	userSource := f.String("user-source", "", "legacy user directory")
	orgSource := f.String("organization-source", "", "legacy organization directory")
	spacesRoot := f.String("spaces", "spaces", "root containing context homes for the host supervisor")
	supervisorImage := f.String("runtime-image", "hermes-hub:0.3.0-prod", "pinned runtime image for the host supervisor")
	supervisorListen := f.String("supervisor-listen", "127.0.0.1:8765", "private host supervisor address")
	supervisorAuth := f.String("supervisor-auth", supervisorAuthFromEnv(), "private supervisor token")
	warmTTL := f.Duration("warm-ttl", 5*time.Minute, "idle runtime retention")
	maxRuntimes := f.Int("max-runtimes", 8, "maximum running context runtimes")
	spoolDir := f.String("spool", "", "mounted stopped gateway spool for execution migration")
	executionMode := f.String("execution-mode", "", "supervisor or static")
	supervisorURL := f.String("supervisor-url", "", "private reachable host supervisor origin")
	nativeCron := f.String("native-cron", "", "disabled, migrated or unmigrated; explicit operator disposition")
	compatibilityRelease := f.String("compatibility-release", "", "release retaining the legacy fallback")
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
	case "execution-audit":
		report, err := migration.AuditExecutionSpool(*spoolDir, *profile)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	case "select-execution":
		return selectExecution(ctx, *dir, *root, *spoolDir, stack.ExecutionSelection{Schema: 1, User: *profile, Environment: *environment, Mode: *executionMode, SupervisorURL: *supervisorURL, NativeCron: *nativeCron, CompatibilityRelease: *compatibilityRelease}, *supervisorAuth, *supervisorImage, *apply)
	case "supervisor":
		if *supervisorAuth == "" {
			return fmt.Errorf("HUB_SUPERVISOR_AUTH or --supervisor-auth is required")
		}
		manager, err := supervisor.New(supervisor.Config{SpacesRoot: *spacesRoot, Image: *supervisorImage, RuntimeAuth: *supervisorAuth, WarmTTL: *warmTTL, MaxConcurrent: *maxRuntimes})
		if err != nil {
			return err
		}
		return supervisor.Serve(ctx, manager, *supervisorListen)
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
			*organizationDir = filepath.Join("spaces", *organization)
		}
		return stack.InitOrganization(*organizationDir, *organization, *profile)
	case "migrate-spaces":
		report, err := migration.Run(migration.Options{Root: *root, User: *profile, Organization: *organization, Environment: *environment, Apply: *apply, UserSource: *userSource, OrganizationSource: *orgSource, StateSource: *stateSource, WorkspaceSource: *workspaceSource})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	case "migrate-toolhub":
		absoluteDir, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		stateDir := *stateSource
		if stateDir == "" {
			stateDir = filepath.Join(absoluteDir, "runtime")
		}
		storePath := *toolHubStore
		if storePath == "" {
			storePath = filepath.Join(stateDir, "toolhub", "store.json")
		}
		storePath, err = filepath.Abs(storePath)
		if err != nil {
			return err
		}
		report, err := migration.MigrateToolHub(migration.ToolHubMigrationOptions{Directory: absoluteDir, StateDir: stateDir, StorePath: storePath, User: *profile, Apply: *apply})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
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
	composePath := filepath.Join(abs, "generated", "compose."+*environment+".yaml")
	if _, statErr := os.Stat(composePath); os.IsNotExist(statErr) {
		composePath = filepath.Join(abs, "compose."+*environment+".yaml")
	}
	prefix := []string{"compose", "-f", composePath}
	dockerCmd := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Env = dockerCLIEnv()
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd
	}
	docker := func(a ...string) error {
		return dockerCmd(append(append([]string{}, prefix...), a...)...).Run()
	}
	if op == "build" || op == "up" {
		if os.Getenv("DOCKER_BUILDKIT") == "0" {
			fmt.Fprintln(os.Stderr, "hubctl: ignoring DOCKER_BUILDKIT=0 (classic builder duplicates the hub image on disk)")
		}
		if err = docker("build"); err != nil {
			return err
		}
		pruneDanglingImages(ctx)
		if op == "build" {
			return nil
		}
		return docker("up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--remove-orphans")
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
		rootAbs, err := filepath.Abs(*root)
		if err != nil {
			return err
		}
		image := "hermes-telegram-account:local"
		if err := dockerCmd("build", "-f", filepath.Join(rootAbs, "docker", "telegram-account.Dockerfile"), "-t", image, rootAbs).Run(); err != nil {
			return err
		}
		return dockerCmd("run", "--rm", "--entrypoint", "/opt/telegram/.venv/bin/python", image, "/opt/telegram/session_string_generator.py", "--phone").Run()
	case "meet-auth":
		return docker("exec", "hermes-runtime", "hermes", "meet", "auth")
	}
	return nil
}

func supervisorAuthFromEnv() string {
	if value := os.Getenv("HUB_SUPERVISOR_AUTH"); value != "" {
		return value
	}
	return os.Getenv("HUB_RUNTIME_AUTH")
}

func dockerCLIEnv() []string {
	return append(os.Environ(), "DOCKER_BUILDKIT=1", "COMPOSE_DOCKER_CLI_BUILD=1", "COMPOSE_BAKE=true")
}

func pruneDanglingImages(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "docker", "image", "prune", "-f")
	cmd.Env = dockerCLIEnv()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}
