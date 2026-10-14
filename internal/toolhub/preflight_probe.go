package toolhub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var publishedImagePull = pullPublishedImage
var publishedMCPToolList = listMCPToolsFromLocalImage
var storedArtifactLoader = LoadStoredOCIArtifact
var localMCPToolList = listMCPToolsFromLocalImage
var networkMCPToolList = networkProbeToolList

// networkTransportFallback gives artifacts that declare an HTTP/SSE transport
// a second probe over their real transport when the stdio probe failed. On
// success the definition is marked so runtime launches the network path too;
// on failure the errors are combined for evidence and the returned list func
// keeps any credential-promotion retry on the network probe.
func networkTransportFallback(ctx context.Context, imported ImportedArtifact, firstErr error, list func(context.Context, ImportedArtifact) ([]ToolSpec, error)) ([]ToolSpec, ImportedArtifact, func(context.Context, ImportedArtifact) ([]ToolSpec, error), error) {
	kind := declaredNetworkTransport(imported)
	if kind == "" || firstErr == nil {
		return nil, imported, list, firstErr
	}
	tools, err := networkMCPToolList(ctx, imported)
	if err == nil {
		imported.Definition.Source.NetworkTransport = kind
		return tools, imported, networkMCPToolList, nil
	}
	return nil, imported, networkMCPToolList, fmt.Errorf("%v [network transport probe: %v]", firstErr, err)
}

// retryListWithPromotedCredentials gives the isolated tools/list probe one
// evidence-based second chance: manifests declare some credentials optional
// because an interactive alternative exists upstream, but a container without
// a browser cannot run it. If injecting every declared-but-optional secret as
// a placeholder lets the server answer tools/list, the probe has proven those
// credentials gate startup and they are promoted to required inputs.
func retryListWithPromotedCredentials(ctx context.Context, imported ImportedArtifact, firstErr error, list func(context.Context, ImportedArtifact) ([]ToolSpec, error)) (ImportedArtifact, []ToolSpec, error) {
	// Servers that gate startup on auth usually name the missing variable in
	// their failure output ("set FOO_TOKEN", "FOO_TOKEN is required"). When the
	// error names credentials, promote exactly those; alternative auth methods
	// declared optional must stay optional. When the error names nothing, fall
	// back to promoting every declared-but-optional secret.
	named := map[string]bool{}
	for _, name := range authErrorCredentialNames(firstErr) {
		named[name] = true
	}
	imported.Definition.Credentials = append([]CredentialInput(nil), imported.Definition.Credentials...)
	changed := false
	for _, name := range authErrorCredentialNames(firstErr) {
		if !credentialNameDeclared(imported.Definition.Credentials, name) {
			imported.Definition.Credentials = append(imported.Definition.Credentials, CredentialInput{Name: name, Required: true})
			changed = true
		}
	}
	for index := range imported.Definition.Credentials {
		input := &imported.Definition.Credentials[index]
		if !input.Required && secretName(input.Name) && (len(named) == 0 || named[input.Name]) {
			input.Required = true
			changed = true
		}
	}
	if !changed {
		return imported, nil, fmt.Errorf("%w: no declared credential to retry with", ErrInvalid)
	}
	tools, err := list(ctx, imported)
	if err != nil {
		return ImportedArtifact{}, nil, err
	}
	return imported, tools, nil
}

func pullPublishedImage(ctx context.Context, image, digest string) error {
	if err := validateLocalImageName(image); err != nil || !digestPattern.MatchString(digest) {
		return fmt.Errorf("%w: published image reference", ErrInvalid)
	}
	dockerConfig, err := os.MkdirTemp("", "hermes-published-docker-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dockerConfig)
	command := exec.CommandContext(ctx, "docker", "pull", "--quiet", image+"@"+digest) // #nosec G204 -- image is digest-pinned and validated.
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_CONFIG=" + dockerConfig}
	output := &boundedOutput{limit: 1 << 20, exceeded: make(chan struct{})}
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil || output.overflow {
		return fmt.Errorf("%w: published image pull failed", ErrIsolation)
	}
	return nil
}

var helpEnvironmentLine = regexp.MustCompile(`^\s*([A-Z][A-Z0-9_]{0,63})\s+(.+)$`)

func discoverMCPHelpCredentials(ctx context.Context, imported ImportedArtifact) []CredentialInput {
	if validateLocalImageName(imported.Definition.Source.Image) != nil {
		return nil
	}
	name, err := randomPreflightContainerName("hermes-help-")
	if err != nil {
		return nil
	}
	args := preflightDockerRunArgs(imported, name, []string{"--rm", "-i"}, []string{"--help"})
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- image and fixed isolation flags are validated above.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	output := &boundedOutput{limit: 64 << 10, exceeded: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil || output.overflow {
		return nil
	}
	return parseMCPHelpCredentials(output.buffer.String())
}

func parseMCPHelpCredentials(help string) []CredentialInput {
	var credentials []CredentialInput
	inEnvironment := false
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "Environment Variables:") {
			inEnvironment = true
			continue
		}
		if !inEnvironment {
			continue
		}
		if trimmed == "" {
			if len(credentials) > 0 {
				break
			}
			continue
		}
		match := helpEnvironmentLine.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		description := strings.ToLower(match[2])
		if strings.Contains(description, "required") || strings.Contains(description, "recommended") {
			credentials = append(credentials, CredentialInput{Name: match[1], Required: true})
		}
	}
	return credentials
}

func restampFromMCPList(ctx context.Context, imported ImportedArtifact, list func(context.Context, ImportedArtifact) ([]ToolSpec, error)) (ImportedArtifact, ConfirmedToolContract, error) {
	if list == nil {
		return ImportedArtifact{}, ConfirmedToolContract{}, ErrInvalid
	}
	tools, err := list(ctx, imported)
	if err != nil {
		return ImportedArtifact{}, ConfirmedToolContract{}, err
	}
	contract, err := ApplyPreflightToolList(&imported, tools)
	if err != nil {
		return ImportedArtifact{}, ConfirmedToolContract{}, err
	}
	return imported, contract, nil
}

func listMCPToolsFromLocalImage(ctx context.Context, imported ImportedArtifact) ([]ToolSpec, error) {
	if err := validateLocalImageName(imported.Definition.Source.Image); err != nil {
		return nil, err
	}
	dockerConfig, err := os.MkdirTemp("", "hermes-preflight-docker-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dockerConfig)
	name, err := randomPreflightContainerName("hermes-preflight-")
	if err != nil {
		return nil, err
	}
	credentialArgs, cleanup, err := preflightCredentialArgs(ctx, imported)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if probeOptionsFrom(ctx).allowNetwork {
		if entry, ok, lookupErr := preparedForSource(ArtifactSource{Repository: imported.Definition.Source.Repository, CommitSHA: imported.Definition.Source.CommitSHA, Subfolder: imported.Definition.Source.Subfolder}); lookupErr != nil {
			return nil, lookupErr
		} else if ok && entry.AuthBeforeToolsList {
			network, proxyURL, stop, err := startPreparedProbeProxy(ctx, imported.Definition.Execution.Egress)
			if err != nil {
				return nil, err
			}
			defer stop()
			credentialArgs = append(credentialArgs, "--network", network, "--env", "HTTP_PROXY="+proxyURL, "--env", "HTTPS_PROXY="+proxyURL)
			credentialArgs = append(credentialArgs, proxyEnvironmentArgs(imported.Definition, proxyURL)...)
		}
	}
	args := preflightDockerRunArgs(imported, name, []string{"--rm", "-i"}, preflightCommandArgs(imported), credentialArgs...)
	if probeOptionsFrom(ctx).allowNetwork {
		// The unauthenticated probe stays on --network none. A second probe
		// after the owner submits real credentials may need egress: some
		// servers authenticate with their API before they speak MCP.
		args = withoutNetworkNone(args)
	}
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- image name is catalog-validated; flags are fixed.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_CONFIG=" + dockerConfig}
	return runMCPProbeIO(cmd, func() { reapPreflightContainer(name) })
}

// runMCPProbeIO drives the initialize + tools/list exchange over a probe
// process's stdin/stdout. reap cleans up probe containers left running after
// a deadline kill; it may be nil for probe shapes that clean up another way.
func runMCPProbeIO(cmd *exec.Cmd, reap func()) ([]ToolSpec, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: MCP preflight start: %v", ErrIsolation, err)
	}
	// Wait joins the stderr-copy goroutine, so the buffer may only be read
	// after it returns; error paths stop the process before reading stderr.
	// Killing the docker client does not remove the container, and Wait can
	// sit past the probe deadline while that process keeps the secret.
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			done := make(chan struct{})
			go func() {
				_ = cmd.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
			if reap != nil {
				reap()
			}
		})
	}
	defer stop()
	enc := json.NewEncoder(stdin)
	for _, msg := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "hub-artifact-preflight", "version": "0.3.0"}}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
	} {
		if err := enc.Encode(msg); err != nil {
			stop()
			return nil, fmt.Errorf("%w: MCP preflight write: %v: %s", ErrIsolation, err, stderr.String())
		}
	}
	// Keep stdin open while decoding: some servers treat stdin EOF as a session
	// shutdown and drop the queued tools/list response.
	tools, err := decodeMCPToolList(stdout)
	if err != nil {
		stop()
		return nil, fmt.Errorf("%w: MCP tools/list: %v: %s", ErrIsolation, err, stderr.String())
	}
	return tools, nil
}

// networkProbeToolList is the last-resort probe for artifacts whose entrypoint
// declares an HTTP/SSE transport and that cannot answer MCP on stdio. The real
// server runs detached with its original args; a second container attaches to
// its network namespace (--network container:) and relays stdin/stdout MCP to
// the discovered loopback listener via hubctl mcp-bridge.
func networkProbeToolList(ctx context.Context, imported ImportedArtifact) ([]ToolSpec, error) {
	if err := validateLocalImageName(imported.Definition.Source.Image); err != nil {
		return nil, err
	}
	if declaredNetworkTransport(imported) == "" {
		return nil, fmt.Errorf("%w: artifact declares no network transport", ErrInvalid)
	}
	bridgeHost, stageCleanup, err := stagePreflightBridge()
	if err != nil {
		return nil, err
	}
	defer stageCleanup()
	dockerConfig, err := os.MkdirTemp("", "hermes-preflight-docker-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dockerConfig)
	serverName, err := randomPreflightContainerName("hermes-preflight-net-")
	if err != nil {
		return nil, err
	}
	bridgeName, err := randomPreflightContainerName("hermes-preflight-net-")
	if err != nil {
		return nil, err
	}
	credentialArgs, credCleanup, err := preflightCredentialArgs(ctx, imported)
	if err != nil {
		return nil, err
	}
	defer credCleanup()
	env := []string{"PATH=" + os.Getenv("PATH"), "DOCKER_CONFIG=" + dockerConfig}
	// The server keeps its reviewed entrypoint: the bridge, not the arg list,
	// is what adapts the network transport to the stdin probe driver.
	serverArgs := preflightDockerRunArgs(imported, serverName, []string{"-d"}, importedCommandArgs(imported), credentialArgs...)
	if probeOptionsFrom(ctx).allowNetwork {
		serverArgs = withoutNetworkNone(serverArgs)
	}
	server := exec.CommandContext(ctx, "docker", serverArgs...) // #nosec G204 -- image name is catalog-validated; flags are fixed.
	server.Env = env
	if body, err := server.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%w: network preflight server start: %s", ErrIsolation, boundedProbeText(string(body)))
	}
	defer reapPreflightContainer(serverName)
	serverLogs := func() string {
		logCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		out, err := exec.CommandContext(logCtx, "docker", "logs", "--tail", "40", serverName).CombinedOutput() // #nosec G204 -- fixed probe container name.
		if err != nil {
			return ""
		}
		return boundedProbeText(string(out))
	}
	image := imported.Definition.Source.Image
	if imported.Definition.Source.ArchiveDigest == "" {
		image += "@" + imported.Definition.Source.Digest
	}
	bridgeArgs := []string{
		"run", "--rm", "-i",
		"--name", bridgeName,
		"--label", "hermes-hub.role=artifact-preflight",
		"--user", "10001:10001",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--network", "container:" + serverName,
		"--cpus", "0.5",
		"--memory", "128m", "--memory-swap", "128m",
		"--pids-limit", "32",
		"--tmpfs", "/tmp:rw,nosuid,nodev,uid=10001,gid=10001,mode=0700,size=16m",
		"--env", "HOME=/tmp",
		"--mount", "type=bind,source=" + bridgeHost + ",target=/hermes-bridge/hubctl,readonly",
		"--entrypoint", "/hermes-bridge/hubctl",
		image, "mcp-bridge", "--wait", "25s", "--transport", "auto",
	}
	cmd := exec.CommandContext(ctx, "docker", bridgeArgs...) // #nosec G204 -- image name is catalog-validated; flags are fixed.
	cmd.Env = env
	tools, err := runMCPProbeIO(cmd, func() {
		reapPreflightContainer(bridgeName)
		reapPreflightContainer(serverName)
	})
	if err != nil {
		if logs := serverLogs(); logs != "" {
			return nil, fmt.Errorf("%v [server logs: %s]", err, logs)
		}
	}
	return tools, err
}

// stagePreflightBridge copies the Linux hubctl binary into a host-visible
// state tempdir so it can be bind-mounted read-only into the probe bridge
// container. HUB_DOCKER_HOST_ROOT must map the state root; without it the
// daemon cannot see the staged binary.
func stagePreflightBridge() (string, func(), error) {
	root := os.Getenv("HUB_STATE")
	directory, err := os.MkdirTemp(root, "hermes-preflight-bridge-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	staged := filepath.Join(directory, "hubctl")
	binary, err := resolveLinuxBridgeBinary("")
	if err == nil {
		err = copyFileMode(binary, staged, 0755)
	} else {
		// Dev hosts (Windows/macOS toolhub builds) cannot copy their own PE/Mach-O
		// binary; cross-compile the same hubctl instead.
		err = BuildLinuxCompanionBridge(staged)
	}
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("%w: network-transport probe bridge binary: %v", ErrIsolation, err)
	}
	host := dockerBindSource(staged)
	if host == "" {
		cleanup()
		return "", func() {}, fmt.Errorf("%w: network-transport probe needs HUB_DOCKER_HOST_ROOT to stage the bridge binary", ErrIsolation)
	}
	return host, cleanup, nil
}

func copyFileMode(source, target string, mode os.FileMode) error {
	body, err := os.ReadFile(source) // #nosec G304 -- resolved bridge binary path.
	if err != nil {
		return err
	}
	return os.WriteFile(target, body, mode)
}

func reapPreflightContainer(name string) {
	if !strings.HasPrefix(name, "hermes-preflight-") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}

func randomPreflightContainerName(prefix string) (string, error) {
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(nonce[:]), nil
}

func decodeMCPToolList(r io.Reader) ([]ToolSpec, error) {
	// MCP stdio is newline-delimited, but servers like slack-mcp-server also
	// print startup logs as JSON ({"level":"error","error":"..."}) or
	// plain text on stdout. Scan line by line and skip anything that is not a
	// JSON-RPC response envelope; only the tools/list reply (id 2) counts.
	// Skipped lines are kept so an auth fatal on stdout is evidence, not silence.
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var skipped strings.Builder
	note := func(line []byte) {
		if skipped.Len() > 2048 || len(line) == 0 {
			return
		}
		if skipped.Len() > 0 {
			skipped.WriteByte('\n')
		}
		skipped.Write(line)
	}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			note(line)
			continue
		}
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			note(line)
			continue
		}
		if len(envelope.ID) == 0 && len(envelope.Result) == 0 {
			note(line)
			continue
		}
		if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
			var rpcErr struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(envelope.Error, &rpcErr)
			if rpcErr.Message == "" {
				rpcErr.Message = string(envelope.Error)
			}
			return nil, fmt.Errorf("%w: MCP error %s", ErrIsolation, rpcErr.Message)
		}
		if strings.TrimSpace(string(envelope.ID)) != "2" {
			continue
		}
		var listed struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
				Annotations struct {
					ReadOnly    bool `json:"readOnlyHint"`
					Destructive bool `json:"destructiveHint"`
				} `json:"annotations"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(envelope.Result, &listed); err != nil || len(listed.Tools) == 0 {
			return nil, fmt.Errorf("%w: MCP tools/list empty (%w): %v: %s", ErrIsolation, errEmptyToolList, err, boundedProbeText(skipped.String()))
		}
		tools := make([]ToolSpec, 0, len(listed.Tools))
		for _, tool := range listed.Tools {
			name := strings.TrimSpace(tool.Name)
			if !mcpToolNamePattern.MatchString(name) {
				return nil, fmt.Errorf("%w: MCP tool %q is not a catalog name", ErrInvalid, name)
			}
			effect := WriteEffect
			if tool.Annotations.ReadOnly && !tool.Annotations.Destructive {
				effect = ReadEffect
			}
			if len(tool.InputSchema) > 65536 {
				return nil, fmt.Errorf("%w: MCP input schema too large", ErrInvalid)
			}
			description := tool.Description
			if len(description) > 1024 {
				description = "" // Long upstream prose is optional; the full schema is not.
			}
			tools = append(tools, ToolSpec{Name: name, Effect: effect, Description: description, InputSchema: tool.InputSchema})
		}
		return tools, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: MCP tools/list stream: %v: %s", ErrIsolation, err, boundedProbeText(skipped.String()))
	}
	return nil, fmt.Errorf("%w: MCP tools/list empty: %s", ErrIsolation, boundedProbeText(skipped.String()))
}

func boundedProbeText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 1500 {
		text = text[:1500]
	}
	return text
}
