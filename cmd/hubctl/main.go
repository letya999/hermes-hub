package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/companion"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log"
	"net/http"
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
	log.SetFlags(log.LstdFlags | log.LUTC)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hubctl:", err)
		os.Exit(1)
	}
}
func warmTTLFlag(f *flag.FlagSet) *time.Duration {
	return f.Duration("warm-ttl", supervisor.DefaultWarmTTL, "idle runtime retention")
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Println("hubctl 0.4.0: init | org-init | migrate-spaces | migrate-toolhub | execution-audit | select-execution | render | doctor | catalog | artifact | build | dev-rebuild | up | down | logs | chat | telegram-login | meet-auth | tools | companion | supervisor | secret | grant | governance | capability | context | memory | skill | routine | apply | usage | watch | image | replay\nFlags: --dir spaces/me --root . --user me --org acme --env prod\nConnector controllers: local-controller (fixed Telegram) or generic-controller (trusted artifact)\nSee README.md for account setup and private VPS access.")
		return nil
	}
	op := args[0]
	if op == "dev-rebuild" {
		return runDevRebuild(ctx, args[1:])
	}
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
	if op == "mcp-bridge" {
		f := flag.NewFlagSet("mcp-bridge", flag.ContinueOnError)
		wait := f.Duration("wait", 30*time.Second, "how long to wait for a loopback MCP listener")
		kind := f.String("transport", "auto", "declared network transport: auto, sse or streamable-http")
		endpointPath := f.String("path", "", "fixed MCP endpoint path (default: probe common paths)")
		port := f.Int("port", 0, "fixed MCP listener port (default: discover from the kernel listen table)")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *wait <= 0 {
			return fmt.Errorf("mcp-bridge accepts no positional arguments and --wait > 0")
		}
		return companion.RunBridge(ctx, *wait, *kind, *endpointPath, *port)
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
	if op == "credentials" {
		return runCredentials(ctx, args[1:])
	}
	if op == "grant" {
		return runGrant(ctx, args[1:])
	}
	if op == "governance" {
		return runGovernance(ctx, args[1:])
	}
	if op == "capability" {
		return runCapability(args[1:])
	}
	if op == "tools-exec" {
		return runToolsExec(ctx)
	}
	if op == "tools-daemon" {
		return runToolsDaemon(ctx)
	}
	if op == "exec-pack" {
		return runExecPack(ctx)
	}
	if op == "exec-scratch" {
		return runExecScratch(ctx)
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
	if op == "apply" {
		f := flag.NewFlagSet("apply", flag.ContinueOnError)
		file := f.String("f", "", "desired-state file (yaml)")
		profile := f.String("user", "me", "person identifier")
		spoolDir := f.String("spool", "", "communication spool")
		stateDir := f.String("state", "", "runtime state dir (knowledge target)")
		spaceDir := f.String("dir", "", "space directory (required by the space: section)")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *file == "" || *spoolDir == "" {
			return fmt.Errorf("apply requires -f <file> and --spool <dir>")
		}
		rep, err := applyFile(*spoolDir, *stateDir, *profile, *file, *spaceDir)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(rep)
	}
	if op == "usage" {
		f := flag.NewFlagSet("usage", flag.ContinueOnError)
		spoolDir := f.String("spool", "", "communication spool dir holding usage.jsonl")
		days := f.Int("days", 0, "window in days (0 = all records)")
		actor := f.String("actor", "", "aggregate only this actor/user id")
		model := f.String("model", "", "aggregate only this model id")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *spoolDir == "" || f.NArg() != 0 {
			return fmt.Errorf("usage requires --spool <dir> and no positional arguments")
		}
		return runUsage(os.Stdout, *spoolDir, time.Duration(*days)*24*time.Hour, *actor, *model)
	}
	if op == "watch" {
		f := flag.NewFlagSet("watch", flag.ContinueOnError)
		job := f.String("job", "", "job id to watch to terminal")
		scope := f.String("scope", "", "user scope to watch (runtime transitions)")
		spoolDir := f.String("spool", "", "communication spool dir (durable mapping source)")
		supervisorURL := f.String("supervisor-url", "", "supervisor HTTP base URL")
		supervisorAuth := f.String("supervisor-auth", "", "supervisor bearer token")
		interval := f.Duration("interval", time.Second, "poll interval")
		timeout := f.Duration("timeout", 0, "overall deadline (e.g. 30s); 0 = wait until done/ctrl-c")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if (*job == "") == (*scope == "") || f.NArg() != 0 || (*spoolDir == "" && *supervisorURL == "") {
			return fmt.Errorf("usage: hubctl watch (--job <id> | --scope <user>) (--spool <dir> | --supervisor-url <url> [--supervisor-auth <tok>]) [--timeout <d>]")
		}
		return runWatch(ctx, os.Stdout, watchOptions{
			Job: *job, Scope: *scope, SpoolDir: *spoolDir,
			SupervisorURL: *supervisorURL, SupervisorAuth: *supervisorAuth,
			Interval: *interval, Timeout: *timeout, Client: http.DefaultClient,
		})
	}
	if op == "image" {
		dir, environment := "", "prod"
		subArgs := args[1:]
		var kept []string
		for i := 0; i < len(subArgs); i++ {
			switch subArgs[i] {
			case "--dir":
				if i+1 >= len(subArgs) {
					return fmt.Errorf("image: --dir requires a value")
				}
				dir, i = subArgs[i+1], i+1
			case "--env":
				if i+1 >= len(subArgs) {
					return fmt.Errorf("image: --env requires a value")
				}
				environment, i = subArgs[i+1], i+1
			default:
				kept = append(kept, subArgs[i])
			}
		}
		if environment != "dev" && environment != "prod" {
			return fmt.Errorf("image requires --env dev|prod")
		}
		return runImage(ctx, os.Stdout, dir, environment, devcheck.DockerSmoke, kept)
	}
	if op == "replay" {
		if len(args) < 2 {
			return fmt.Errorf("usage: hubctl replay export --job <id> --spool <dir> [-o <dir>] | hubctl replay run --bundle <file> --replay-dir <dir> [--runtime-url <url>]")
		}
		switch args[1] {
		case "export":
			f := flag.NewFlagSet("replay export", flag.ContinueOnError)
			job := f.String("job", "", "durable job id to export")
			spoolDir := f.String("spool", "", "communication spool dir holding the job")
			spaceDir := f.String("space", "", "space dir for non-secret doc snapshot")
			outDir := f.String("o", "", "output dir for bundle.json")
			promptFile := f.String("prompt-file", "", "prompt file for sensitive jobs (verified against recorded digest)")
			if err := f.Parse(args[2:]); err != nil {
				return err
			}
			if *job == "" || *spoolDir == "" || *outDir == "" || f.NArg() != 0 {
				return fmt.Errorf("usage: hubctl replay export --job <id> --spool <dir> [--space <dir>] -o <dir> [--prompt-file <f>]")
			}
			return runReplayExport(os.Stdout, replayExportOpts{
				JobID: *job, SpoolDir: *spoolDir, SpaceDir: *spaceDir, OutDir: *outDir, PromptFile: *promptFile,
			})
		case "run":
			f := flag.NewFlagSet("replay run", flag.ContinueOnError)
			bundle := f.String("bundle", "", "bundle.json produced by replay export")
			replayDir := f.String("replay-dir", "", "isolated dir for replay spool + mock captures")
			runtimeURL := f.String("runtime-url", "", "optional replay supervisor URL (/v1/execute)")
			runtimeAuth := f.String("runtime-auth", "", "optional bearer token for --runtime-url")
			if err := f.Parse(args[2:]); err != nil {
				return err
			}
			if *bundle == "" || *replayDir == "" || f.NArg() != 0 {
				return fmt.Errorf("usage: hubctl replay run --bundle <file> --replay-dir <dir> [--runtime-url <url> [--runtime-auth <tok>]]")
			}
			return runReplayRun(os.Stdout, replayRunOpts{
				BundlePath: *bundle, ReplayDir: *replayDir, RuntimeURL: *runtimeURL, RuntimeAuth: *runtimeAuth,
			})
		default:
			return fmt.Errorf("replay: unknown subcommand %q (export|run)", args[1])
		}
	}
	if op == "spool" {
		f := flag.NewFlagSet("spool", flag.ContinueOnError)
		spoolDir := f.String("spool", "", "communication spool dir")
		stateDir := f.String("state", "", "runtime state dir (knowledge target)")
		apply := f.Bool("apply", false, "apply migration; default is dry-run")
		if len(args) < 2 || args[1] != "migrate" {
			return fmt.Errorf("usage: hubctl spool migrate --spool <dir> --state <dir> [--apply]")
		}
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *spoolDir == "" || *stateDir == "" {
			return fmt.Errorf("usage: hubctl spool migrate --spool <dir> --state <dir> [--apply]")
		}
		rep, err := spoolMigrate(*spoolDir, *stateDir, *apply)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(rep)
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
	noBuild := f.Bool("no-build", false, "start existing images without rebuilding (up only)")
	live := f.Bool("live", false, "doctor: verify the selected model against the live provider catalog")
	stateSource := f.String("state-volume", "", "legacy runtime state directory")
	toolHubStore := f.String("toolhub-store", "", "absolute ToolHub registry path")
	workspaceSource := f.String("workspace-volume", "", "legacy workspace directory")
	userSource := f.String("user-source", "", "legacy user directory")
	orgSource := f.String("organization-source", "", "legacy organization directory")
	spacesRoot := f.String("spaces", "spaces", "root containing context homes for the host supervisor")
	supervisorImage := f.String("runtime-image", "hermes-hub:0.4.0-prod", "pinned runtime image for the host supervisor")
	supervisorListen := f.String("supervisor-listen", "127.0.0.1:8876", "private host supervisor address")
	supervisorWildcard := f.Bool("supervisor-allow-wildcard-listen", false, "permit binding the supervisor control plane on all interfaces")
	supervisorAuth := f.String("supervisor-auth", supervisorAuthFromEnv(), "private supervisor token")
	warmTTL := warmTTLFlag(f)
	maxRuntimes := f.Int("max-runtimes", 8, "maximum running context runtimes")
	spoolDir := f.String("spool", "", "mounted stopped gateway spool for execution migration")
	executionMode := f.String("execution-mode", "", "supervisor or static")
	supervisorURL := f.String("supervisor-url", "", "private reachable host supervisor origin")
	nativeCron := f.String("native-cron", "", "disabled, migrated or unmigrated; explicit operator disposition")
	compatibilityRelease := f.String("compatibility-release", "", "release retaining the legacy fallback")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 && op != "exec" && op != "build" && op != "up" && op != "trace" {
		return fmt.Errorf("unexpected arguments")
	}
	if op == "trace" && f.NArg() != 1 {
		return fmt.Errorf("trace requires exactly one job id")
	}
	if op == "build" || op == "up" {
		for _, service := range f.Args() {
			if strings.HasPrefix(service, "-") {
				return fmt.Errorf("invalid service %q", service)
			}
		}
	}
	if *noBuild && op != "up" {
		return fmt.Errorf("--no-build is only valid with up")
	}
	if *live && op != "doctor" {
		return fmt.Errorf("--live is only valid with doctor")
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
		// -env defaults to "prod" for render/up commands, but the supervisor
		// must honor HUB_ENV when the flag is absent: leaving Environment
		// empty lets Manager.environment() fall back to it.
		supervisorEnv := *environment
		envSet := false
		f.Visit(func(fl *flag.Flag) {
			if fl.Name == "env" {
				envSet = true
			}
		})
		if !envSet {
			supervisorEnv = ""
		}
		stopLog, err := diagnostics.HostLog(filepath.Join(filepath.Dir(*spacesRoot), ".local", "supervisor.log"))
		if err != nil {
			return err
		}
		defer stopLog()
		manager, err := supervisor.New(supervisor.Config{SpacesRoot: *spacesRoot, Image: *supervisorImage, RuntimeAuth: *supervisorAuth, Environment: supervisorEnv, WarmTTL: *warmTTL, MaxConcurrent: *maxRuntimes, AllowWildcardListen: *supervisorWildcard})
		if err != nil {
			return err
		}
		return supervisor.Serve(ctx, manager, *supervisorListen)
	case "init":
		if *organization != "" {
			return stack.InitEnvironmentWithOrganization(*dir, *profile, *environment, *organization)
		}
		return stack.InitEnvironment(*dir, *profile, *environment)
	case "group-init":
		return stack.InitGroupEnvironment(*dir, *profile, *environment, *organization)
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
	case "trace":
		explicitUser := ""
		f.Visit(func(fl *flag.Flag) {
			if fl.Name == "user" {
				explicitUser = *profile
			}
		})
		return runTrace(ctx, os.Stdout, f.Arg(0), explicitUser, *spacesRoot)
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
			if s.CapabilityMode == "managed" {
				modelSecrets := map[string]string{}
				for key, value := range orgSecrets {
					modelSecrets[key] = value
				}
				for key, value := range secrets {
					modelSecrets[key] = value
				}
				plan, err := stack.ModelDoctor(*dir, s, modelSecrets)
				if err != nil {
					return err
				}
				for _, line := range plan {
					fmt.Println(line)
				}
			}
			fmt.Printf("Effective model: owner=%s model=%s endpoint=%s proxy=%q credential_scope=%q (empty: shared proxy / undeclared legacy credential scope).\n", s.User, s.Model, s.ModelURL, s.Cliproxy, s.CliproxyCredentials)
			fmt.Println("Model restart policy: rendered registry changes hot-reload in the owner's relay; provider env/auth changes require recreating that relay; proxy placement changes require that owner's relay/proxy; a changed runtime model applies at the next owner runtime spawn.")
			if *live {
				key := secrets["OPENAI_API_KEY"]
				if key == "" {
					key = orgSecrets["OPENAI_API_KEY"]
				}
				client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
					return fmt.Errorf("model catalog redirects are not followed")
				}}
				composePath := filepath.Join(*dir, "generated", "compose."+*environment+".yaml")
				if _, statErr := os.Stat(composePath); os.IsNotExist(statErr) { // #nosec G703 -- operator-supplied project dir
					composePath = filepath.Join(*dir, "compose."+*environment+".yaml")
				}
				portLookup := doctorPortLookup(*dir, func(ctx context.Context, env, args []string) ([]byte, error) {
					cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G702 -- fixed docker executable, argv without shell
					cmd.Env = env
					return cmd.CombinedOutput()
				})
				if err := doctorLiveModel(ctx, s.ModelURL, key, s.Model, composePath, portLookup, client); err != nil {
					return err
				}
				fmt.Println("Selected model is present in the provider catalog.")
				return nil
			}
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
	if _, statErr := os.Stat(composePath); os.IsNotExist(statErr) { // #nosec G703 -- operator-supplied project dir
		composePath = filepath.Join(abs, "compose."+*environment+".yaml")
	}
	prefix := []string{"compose", "-f", composePath}
	composeEnv := supervisorComposeEnv(abs)
	dockerCmd := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G702 -- fixed docker executable, argv without shell
		cmd.Env = composeEnv
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd
	}
	docker := func(a ...string) error {
		return dockerCmd(append(append([]string{}, prefix...), a...)...).Run()
	}
	if op == "build" || op == "up" {
		// Resolve required variables and env files before any Docker mutation.
		// Quiet mode validates without printing the resolved credentials.
		if err := docker("config", "--quiet"); err != nil {
			return fmt.Errorf("compose configuration preflight: %w", err)
		}
		if op == "up" {
			s, readErr := stack.ReadEnvironment(*dir, *environment)
			if readErr != nil {
				return readErr
			}
			if s.RendersInfra() {
				if err := checkProviderAliasOwnership(ctx, "hermes-hub-"+s.User+"-"+*environment, dockerOutput); err != nil {
					return err
				}
			}
			if s.Has("telegram") {
				secrets, secretErr := stack.ReadSecrets(filepath.Join(*dir, "secrets."+*environment+".env"))
				if secretErr != nil {
					return secretErr
				}
				if err := checkTelegramOwnership(ctx, "hermes-hub-"+s.User+"-"+*environment, secrets["TELEGRAM_BOT_TOKEN"], dockerOutput); err != nil {
					return err
				}
			}
		}
		if !*noBuild {
			if os.Getenv("DOCKER_BUILDKIT") == "0" {
				fmt.Fprintln(os.Stderr, "hubctl: ignoring DOCKER_BUILDKIT=0 (classic builder duplicates the hub image on disk)")
			}
			// A previous build may have been killed before its cleanup ran.
			if err := pruneDanglingImages(ctx, dockerOutput); err != nil {
				return err
			}
			defer func() {
				// A failed build can also leave dangling layers and stopped containers.
				// Cleanup must not hide the original build/up error.
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				if cleanupErr := pruneDanglingImages(cleanupCtx, dockerOutput); cleanupErr != nil {
					fmt.Fprintln(os.Stderr, "hubctl: Docker cleanup:", cleanupErr)
				}
			}()
			if err = docker(append([]string{"build"}, f.Args()...)...); err != nil {
				return err
			}
			// CLI sandbox cells run on dedicated images the controller
			// resolves by tag at start; compose services never build them.
			rootAbs, err := filepath.Abs(*root)
			if err != nil {
				return err
			}
			dockerfile := filepath.Join(rootAbs, "docker", "Dockerfile")
			for _, stage := range []string{"cli-tools", "cell-proxy"} {
				image := "hermes-hub-" + stage + ":0.4.0-" + *environment
				if err := dockerCmd("build", "--target", stage, "-t", image, "-f", dockerfile, rootAbs).Run(); err != nil {
					return fmt.Errorf("build %s: %w", image, err)
				}
			}
			if *environment == "dev" && (len(f.Args()) == 0 || slices.Contains(f.Args(), "cliproxy")) {
				for _, pin := range []string{"dev-runtime-image", "dev-script-image"} {
					if err := os.Remove(filepath.Join(abs, "generated", pin)); err != nil && !os.IsNotExist(err) { // #nosec G703 -- operator-supplied project dir
						return err
					}
				}
			}
		}
		if op == "build" {
			return nil
		}
		s, readErr := stack.ReadEnvironment(*dir, *environment)
		if readErr != nil {
			return readErr
		}
		if s.RendersInfra() && (len(f.Args()) == 0 || slices.Contains(f.Args(), "credential-broker")) {
			for _, vol := range []string{"hermes-credential-broker-recovery-", "hermes-credential-broker-recovery-key-"} {
				if err := dockerCmd("volume", "create", vol+*environment).Run(); err != nil {
					return fmt.Errorf("provision broker recovery volume: %w", err)
				}
			}
		}
		upArgs := []string{"up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--remove-orphans"}
		if *noBuild {
			upArgs = append(upArgs, "--no-build")
		}
		if len(f.Args()) > 0 {
			upArgs = append(upArgs, "--no-deps")
		}
		if err := docker(append(upArgs, f.Args()...)...); err != nil {
			return err
		}
		return nil
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
	return append(os.Environ(), devcheck.BuildKitEnv...)
}

// supervisorComposeEnv feeds supervisor.auth and media.grant into compose
// variable interpolation. The rendered ${HUB_SUPERVISOR_AUTH} and
// ${HUB_MEDIA_BROKER_GRANT} placeholders have no value otherwise; an explicit
// process env always wins over the file.
func supervisorComposeEnv(dir string) []string {
	env := dockerCLIEnv()
	if os.Getenv("HUB_SUPERVISOR_AUTH") == "" {
		if auth, err := stack.ReadSecrets(filepath.Join(dir, "supervisor.auth")); err == nil {
			if token := strings.TrimSpace(auth["HUB_SUPERVISOR_AUTH"]); token != "" {
				env = append(env, "HUB_SUPERVISOR_AUTH="+token)
			}
		}
	}
	if os.Getenv("HUB_MEDIA_BROKER_GRANT") == "" {
		// media.grant is not a secret: it is the grant ID provisioned for this
		// space's hub-media service (docs/operations.md, media credentials).
		if auth, err := stack.ReadSecrets(filepath.Join(dir, "media.grant")); err == nil {
			if grant := strings.TrimSpace(auth["HUB_MEDIA_BROKER_GRANT"]); grant != "" {
				env = append(env, "HUB_MEDIA_BROKER_GRANT="+grant)
			}
		}
	}
	return env
}

func dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G702 -- fixed docker executable, argv without shell
	cmd.Env = dockerCLIEnv()
	return cmd.CombinedOutput()
}

// A stopped Compose container can pin the previous generation after its tag
// moves. Remove only this project's stopped containers on dangling images.
func pruneDanglingImages(ctx context.Context, run func(context.Context, ...string) ([]byte, error)) error {
	ids, err := run(ctx, "image", "ls", "-q", "--filter", "dangling=true")
	if err != nil {
		return fmt.Errorf("list dangling images: %w", err)
	}
	for _, id := range strings.Fields(string(ids)) {
		body, err := run(ctx, "ps", "-a", "--filter", "ancestor="+id, "--format", "{{.Names}}\t{{.Status}}")
		if err != nil {
			return fmt.Errorf("list containers for %s: %w", id, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			name, status, _ := strings.Cut(line, "\t")
			if !strings.HasPrefix(name, "hermes-hub-") || strings.HasPrefix(status, "Up") {
				continue
			}
			if _, err := run(ctx, "rm", name); err != nil {
				return fmt.Errorf("remove stale container %s: %w", name, err)
			}
		}
	}
	if _, err := run(ctx, "image", "prune", "-f"); err != nil {
		return fmt.Errorf("prune dangling images: %w", err)
	}
	if _, err := run(ctx, "buildx", "prune", "--max-used-space", "8gb", "-f"); err != nil {
		return fmt.Errorf("limit build cache: %w", err)
	}
	return nil
}
