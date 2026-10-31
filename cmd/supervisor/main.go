package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/supervisor"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hub-supervisor:", err)
		os.Exit(1)
	}
}

type supervisorArgs struct {
	spaces         string
	image          string
	listen         string
	auth           string
	authFile       string
	authFlagSet    bool
	wildcardListen bool
	watch          bool
	environment    string
	warmTTL        time.Duration
	browserIdleTTL time.Duration
	max            int
}

func (o supervisorArgs) config() supervisor.Config {
	return supervisor.Config{SpacesRoot: o.spaces, Image: o.image, Environment: o.environment, RuntimeAuth: o.auth, WarmTTL: o.warmTTL, BrowserIdleTTL: o.browserIdleTTL, MaxConcurrent: o.max, AllowWildcardListen: o.wildcardListen}
}

func parseSupervisorArgs(argv []string) (supervisorArgs, error) {
	var o supervisorArgs
	flags := flag.NewFlagSet("hub-supervisor", flag.ContinueOnError)
	flags.StringVar(&o.spaces, "spaces", "spaces", "root containing isolated context homes")
	flags.StringVar(&o.image, "image", "hermes-hub:0.5.0-prod", "pinned Hermes runtime image")
	flags.StringVar(&o.listen, "listen", envOr("HUB_SUPERVISOR_LISTEN", "127.0.0.1:8876"), "private supervisor listen address")
	flags.StringVar(&o.auth, "auth", supervisorAuthFromEnv(), "shared private supervisor token")
	flags.StringVar(&o.authFile, "auth-file", os.Getenv("HUB_SUPERVISOR_AUTH_FILE"), "file containing the supervisor token (preferred over -auth)")
	flags.BoolVar(&o.wildcardListen, "allow-wildcard-listen", false, "permit binding the control plane on all interfaces")
	flags.BoolVar(&o.watch, "watch", false, "restart a crashed supervisor child within 10 seconds")
	flags.StringVar(&o.environment, "env", envOr("HUB_ENV", "prod"), "runtime environment: dev or prod")
	flags.DurationVar(&o.warmTTL, "warm-ttl", supervisor.DefaultWarmTTL, "idle runtime retention")
	flags.DurationVar(&o.browserIdleTTL, "browser-idle-ttl", supervisor.DefaultBrowserIdleTTL, "idle browser sidecar retention before sleep")
	flags.IntVar(&o.max, "max-concurrent", 8, "maximum running context runtimes")
	if err := flags.Parse(argv); err != nil {
		return o, err
	}
	flags.Visit(func(fl *flag.Flag) {
		if fl.Name == "auth" {
			o.authFlagSet = true
		}
	})
	if !o.wildcardListen {
		host, _, err := net.SplitHostPort(o.listen)
		if err == nil && (host == "" || net.ParseIP(host).IsUnspecified()) {
			return o, fmt.Errorf("wildcard listen %q requires -allow-wildcard-listen; the control plane binds a private address by default", o.listen)
		}
	}
	return o, nil
}

// readSupervisorAuthFile loads the supervisor token from a file that only the
// owner may supply: symlinks, directories, empty files and group/world-readable
// (unix) or out-of-profile (Windows) paths are refused.
func readSupervisorAuthFile(path, spaces string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("auth file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("auth file must be a regular file, not a symlink or directory")
	}
	if runtime.GOOS == "windows" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("auth file: %w", err)
		}
		under := func(root string) bool {
			rel, err := filepath.Rel(root, abs)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		allowed := false
		if home, herr := os.UserHomeDir(); herr == nil && under(home) {
			allowed = true
		}
		if control, serr := filepath.Abs(filepath.Join(spaces, ".control")); serr == nil && under(control) {
			allowed = true
		}
		if !allowed {
			return "", errors.New("auth file must live under the user profile or <spaces>/.control")
		}
	} else if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("auth file must be readable only by its owner (chmod 600)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("auth file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("auth file is empty")
	}
	if len(token) > 4096 {
		return "", errors.New("auth file is oversized")
	}
	return token, nil
}

func (o supervisorArgs) resolveAuth() (string, error) {
	if o.authFile != "" {
		return readSupervisorAuthFile(o.authFile, o.spaces)
	}
	if o.authFlagSet {
		log.Printf("warning: -auth on the command line is deprecated and leaks the token into process listings; use -auth-file")
	}
	return o.auth, nil
}

func run() error {
	o, err := parseSupervisorArgs(os.Args[1:])
	if err != nil {
		return err
	}
	if o.auth, err = o.resolveAuth(); err != nil {
		return err
	}
	if o.auth == "" {
		return errors.New("HUB_SUPERVISOR_AUTH or --auth-file/--auth is required")
	}
	if o.environment != "dev" && o.environment != "prod" {
		return errors.New("supervisor environment must be dev or prod")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.watch {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return watchSupervisor(ctx, executable, append(os.Args[1:], "-watch=false"))
	}
	stopLog, err := diagnostics.HostLog(filepath.Join(filepath.Dir(o.spaces), ".local", "supervisor.log"))
	if err != nil {
		return err
	}
	defer stopLog()
	m, err := supervisor.New(o.config())
	if err != nil {
		return err
	}
	return supervisor.Serve(ctx, m, o.listen)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func supervisorAuthFromEnv() string {
	if value := os.Getenv("HUB_SUPERVISOR_AUTH"); value != "" {
		return value
	}
	return os.Getenv("HUB_RUNTIME_AUTH")
}
