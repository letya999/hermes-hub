package api

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

// PreflightCommandArgs overrides a network transport with stdio. The probe
// writes MCP on stdin; an image command such as "--transport sse" never reads
// that stream, so tools/list waits until the deadline.
func PreflightCommandArgs(imported ImportedArtifact) []string {
	argv := importedCommandArgs(imported)
	rewritten := StdioTransportArgs(argv)
	if slices.Equal(rewritten, argv) {
		return nil
	}
	return rewritten
}

// importedCommandArgs is the artifact's declared argv as reviewed: definition
// source args win, then the recipe entrypoint tail.
func importedCommandArgs(imported ImportedArtifact) []string {
	argv := append([]string{}, imported.Definition.Source.Args...)
	if len(argv) == 0 && len(imported.Recipe.Entrypoint) > 1 {
		argv = append([]string{}, imported.Recipe.Entrypoint[1:]...)
	}
	return argv
}

// declaredNetworkTransport reports the entrypoint's declared network
// transport, normalized: "sse" for legacy SSE, "streamable-http" for the
// modern HTTP transport. Empty means the artifact does not declare one.
func declaredNetworkTransport(imported ImportedArtifact) string {
	return NetworkTransportValue(importedCommandArgs(imported))
}

func NetworkTransportValue(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "-t" && argv[i] != "--transport" {
			continue
		}
		switch strings.ToLower(argv[i+1]) {
		case "sse":
			return "sse"
		case "http", "streamable-http", "streamable_http", "streamablehttp":
			return "streamable-http"
		}
	}
	return ""
}

// StdioTransportArgs rewrites a network transport flag to stdio. The companion
// always speaks MCP on the child's stdio, so the same override used for
// preflight probes applies to the runtime launch as well.
func StdioTransportArgs(argv []string) []string {
	rewritten := append([]string(nil), argv...)
	changed := false
	for i := 0; i+1 < len(rewritten); i++ {
		if rewritten[i] != "-t" && rewritten[i] != "--transport" {
			continue
		}
		switch strings.ToLower(rewritten[i+1]) {
		case "sse", "http", "streamable-http":
			rewritten[i+1] = "stdio"
			changed = true
		}
	}
	if !changed {
		return argv
	}
	return rewritten
}

func PreflightDockerRunArgs(imported ImportedArtifact, name string, runFlags []string, commandArgs []string, extra ...string) []string {
	args := []string{"run"}
	args = append(args, runFlags...)
	args = append(args,
		"--name", name,
		"--label", "hermes-hub.role=artifact-preflight",
		"--user", "10001:10001",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--network", "none",
		"--cpus", "1",
		"--memory", "512m", "--memory-swap", "512m",
		"--pids-limit", "64",
		"--tmpfs", "/tmp:rw,nosuid,nodev,uid=10001,gid=10001,mode=0700,size=64m",
		"--env", "HOME=/tmp",
	)
	for _, mount := range imported.Definition.Execution.Mounts {
		if !strings.HasPrefix(mount.Target, "/") || strings.Contains(mount.Target, "..") {
			continue
		}
		args = append(args, "--tmpfs", mount.Target+":rw,nosuid,nodev,uid=10001,gid=10001,mode=0700,size=16m")
	}
	args = append(args, extra...)
	args = append(args, RuntimeEnvironmentArgs(imported.Definition)...)
	image := imported.Definition.Source.Image
	if imported.Definition.Source.ArchiveDigest == "" {
		image += "@" + imported.Definition.Source.Digest
	}
	args = append(args, image)
	args = append(args, commandArgs...)
	for _, mount := range imported.Definition.Execution.Mounts {
		if !strings.HasPrefix(mount.Target, "/") || strings.Contains(mount.Target, "..") {
			continue
		}
		already := false
		for _, part := range imported.Recipe.Entrypoint {
			if part == mount.Target {
				already = true
				break
			}
		}
		if !already {
			args = append(args, mount.Target)
		}
	}
	return args
}

type ProbeOptions struct {
	Values       map[string]string
	AllowNetwork bool
	Mounts       []Mount
}

type probeOptionsKey struct{}

func WithProbeOptions(ctx context.Context, opts ProbeOptions) context.Context {
	return context.WithValue(ctx, probeOptionsKey{}, opts)
}

func ProbeOptionsFrom(ctx context.Context) ProbeOptions {
	if ctx == nil {
		return ProbeOptions{}
	}
	opts, _ := ctx.Value(probeOptionsKey{}).(ProbeOptions)
	return opts
}

func WithoutNetworkNone(args []string) []string {
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--network" && i+1 < len(args) && args[i+1] == "none" {
			i++
			continue
		}
		filtered = append(filtered, args[i])
	}
	return filtered
}

func PreflightCredentialArgs(ctx context.Context, imported ImportedArtifact) ([]string, func(), error) {
	prepared, _, err := PreparedForSource(ArtifactSource{Repository: imported.Definition.Source.Repository, CommitSHA: imported.Definition.Source.CommitSHA, Subfolder: imported.Definition.Source.Subfolder})
	if err != nil {
		return nil, func() {}, err
	}
	root := os.Getenv("HUB_STATE")
	directory, err := os.MkdirTemp(root, "hermes-preflight-credentials-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) } // #nosec G703 -- fresh MkdirTemp dir
	args := []string{}
	if prepared.StateTarget != "" {
		args = append(args, "--tmpfs", prepared.StateTarget+":rw,nosuid,nodev,uid=10001,gid=10001,mode=0700,size=16m")
	}
	if opts := ProbeOptionsFrom(ctx); opts.Values != nil {
		for _, mount := range opts.Mounts {
			declared := slices.ContainsFunc(imported.Definition.Credentials, func(input CredentialInput) bool {
				return input.Delivery == "file" && input.Target == mount.Target
			})
			info, statErr := os.Lstat(mount.Source)
			source := DockerBindSource(mount.Source)
			if !declared || !mount.ReadOnly || !ValidContainerMountTarget(mount.Target) || NoSymlinkPath(mount.Source) != nil || statErr != nil || !info.Mode().IsRegular() || strings.ContainsAny(source, ",\x00\r\n") {
				cleanup()
				return nil, func() {}, fmt.Errorf("%w: probe credential mount", ErrIsolation)
			}
			args = append(args, "--mount", "type=bind,source="+source+",target="+mount.Target+",readonly")
		}
		// Owner-submitted secrets stay in a 0600 env file, not in process argv
		// and not in the error text. The placeholder "preflight" is only for
		// the unauthenticated probe that has no real value yet.
		var lines []string
		for _, input := range imported.Definition.Credentials {
			value := strings.TrimSpace(opts.Values[input.Name])
			if value == "" {
				continue
			}
			if strings.ContainsAny(value, "\x00\r\n") {
				cleanup()
				return nil, func() {}, fmt.Errorf("%w: credential value", ErrInvalid)
			}
			lines = append(lines, input.Name+"="+value)
		}
		if len(lines) > 0 {
			path := directory + string(os.PathSeparator) + "credentials.env"
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil { // #nosec G703 -- temp dir; credential names validated by CredentialPattern
				cleanup()
				return nil, func() {}, err
			}
			args = append(args, "--env-file", path)
		}
		return args, cleanup, nil
	}
	for _, input := range imported.Definition.Credentials {
		if !input.Required {
			continue
		}
		value := "preflight"
		if strings.HasSuffix(input.Name, "_URL") {
			value = "https://preflight.invalid"
		}
		if body, ok := prepared.PreflightFiles[input.Name]; ok {
			path := directory + string(os.PathSeparator) + input.Name + ".json"
			if err := os.WriteFile(path, body, 0600); err != nil { // #nosec G703 -- temp dir; credential names validated by CredentialPattern
				cleanup()
				return nil, func() {}, err
			}
			target := "/run/hermes-preflight/" + input.Name + ".json"
			args = append(args, "--mount", "type=bind,source="+DockerBindSource(path)+",target="+target+",readonly")
			value = target
		}
		args = append(args, "--env", input.Name+"="+value)
	}
	// Reviewed literals come last so they override any same-named credential
	// placeholder; submitted secrets take the earlier branch instead.
	for _, name := range slices.Sorted(maps.Keys(prepared.PreflightEnvironment)) {
		args = append(args, "--env", name+"="+prepared.PreflightEnvironment[name])
	}
	return args, cleanup, nil
}
