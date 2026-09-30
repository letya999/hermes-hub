package toolhub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// PreflightImportedArtifact loads the quarantine image into local Docker,
// runs tools/list with the MCP runtime profile (not the BuildKit bootstrap
// profile), and restamps the review packet with that confirmed contract.
func PreflightImportedArtifact(ctx context.Context, imported ImportedArtifact, artifactsDir string) (ImportedArtifact, ConfirmedToolContract, error) {
	if _, err := storedArtifactLoader(ctx, artifactsDir, imported.Artifact.ArchiveDigest, imported.Definition.Source.Image, 8<<30); err != nil {
		return ImportedArtifact{}, ConfirmedToolContract{}, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if prepared, ok, _ := preparedForSource(ArtifactSource{Repository: imported.Definition.Source.Repository, CommitSHA: imported.Definition.Source.CommitSHA, Subfolder: imported.Definition.Source.Subfolder}); ok && prepared.PreflightNetwork {
		listCtx = withProbeOptions(listCtx, probeOptions{allowNetwork: true})
	}
	list := localMCPToolList
	tools, err := list(listCtx, imported)
	if err != nil {
		tools, imported, list, err = networkTransportFallback(listCtx, imported, err, list)
	}
	if err != nil {
		promoted, retry, retryErr := retryListWithPromotedCredentials(listCtx, imported, err, list)
		if retryErr != nil {
			combined := fmt.Errorf("%v [credential-promotion retry: %v]", err, retryErr)
			base := imported
			if promoted.Definition.DefinitionID != "" {
				base = promoted
			}
			if gated, gate, ok := credentialGateFrom(base, combined); ok {
				return gated, ConfirmedToolContract{}, gate
			}
			return ImportedArtifact{}, ConfirmedToolContract{}, combined
		}
		imported, tools = promoted, retry
	}
	if len(imported.Definition.Credentials) == 0 {
		imported.Definition.Credentials = discoverMCPHelpCredentials(listCtx, imported)
	}
	return restampFromMCPList(listCtx, imported, func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return tools, nil })
}

// PreflightPublishedArtifact pulls only the reviewed immutable image and runs
// the same isolated MCP initialize/tools-list probe used for local artifacts.
func PreflightPublishedArtifact(ctx context.Context, imported ImportedArtifact) (ImportedArtifact, ConfirmedToolContract, error) {
	if imported.Definition.Source.ArchiveDigest != "" || imported.Definition.Source.Repository == "" || !digestPattern.MatchString(imported.Definition.Source.Digest) {
		return ImportedArtifact{}, ConfirmedToolContract{}, fmt.Errorf("%w: published artifact evidence required", ErrInvalid)
	}
	if err := publishedImagePull(ctx, imported.Definition.Source.Image, imported.Definition.Source.Digest); err != nil {
		return ImportedArtifact{}, ConfirmedToolContract{}, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	list := publishedMCPToolList
	tools, err := list(listCtx, imported)
	if err != nil {
		tools, imported, list, err = networkTransportFallback(listCtx, imported, err, list)
	}
	if err != nil {
		promoted, retry, retryErr := retryListWithPromotedCredentials(listCtx, imported, err, list)
		if retryErr != nil {
			combined := fmt.Errorf("%v [credential-promotion retry: %v]", err, retryErr)
			base := imported
			if promoted.Definition.DefinitionID != "" {
				base = promoted
			}
			if gated, gate, ok := credentialGateFrom(base, combined); ok {
				return gated, ConfirmedToolContract{}, gate
			}
			return ImportedArtifact{}, ConfirmedToolContract{}, combined
		}
		imported, tools = promoted, retry
	}
	return restampFromMCPList(listCtx, imported, func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return tools, nil })
}

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

var authErrorEnvVar = regexp.MustCompile(`\b(?:set|missing|provide|export)\s+(?:the\s+)?(?:environment\s+(?:variable|variable\s+name)\s+)?([A-Z][A-Z0-9_]{2,63})\b|\b([A-Z][A-Z0-9_]{2,63})\s+(?:is\s+required|environment\s+variable\s+is\s+required|not\s+set|must\s+be\s+set)`)
var authFailureText = regexp.MustCompile(`(?i)authentication required|must be provided|must be set|is required|not set|missing credential`)
var envNameInText = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,63}\b`)
var bothCredentialPair = regexp.MustCompile(`both\s+([A-Z][A-Z0-9_]{2,63})\s+and\s+([A-Z][A-Z0-9_]{2,63})`)

// CredentialGate is an isolated tools/list that never started because the
// server named the secrets it needs first. It is not an empty catalog and not
// a confirmed tool contract: onboarding collects those secrets on the
// protected form, then lists tools with the submitted values.
type CredentialGate struct {
	Names  []string
	Groups [][]string
	Detail string
}

func (e *CredentialGate) Error() string {
	if e == nil {
		return "MCP refused tools/list until credentials exist"
	}
	return "MCP refused tools/list until credentials exist: " + strings.Join(e.Names, ", ")
}

func (e *CredentialGate) Unwrap() error { return ErrIsolation }

// authErrorCredentialNames extracts secret-shaped env names a failed server
// printed in its auth error. Only TOKEN/SECRET/KEY-style names are promoted:
// plain config variables mentioned in usage text are not credentials.
func authErrorCredentialNames(err error) []string {
	if err == nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	text := err.Error()
	for _, match := range authErrorEnvVar.FindAllStringSubmatch(text, 8) {
		rememberAuthCredential(&names, seen, match[1])
		rememberAuthCredential(&names, seen, match[2])
	}
	// Servers such as slack-mcp-server exit before MCP with a sentence like
	// "Either FOO_TOKEN, BAR_TOKEN, or both BAZ_TOKEN and QUX_TOKEN must be
	// provided". The names are not in the "set FOO" shape, but they are still
	// the credential gate. Only secret-shaped names count, and only when the
	// text is an auth failure, so usage prose does not become a form.
	if authFailureText.MatchString(text) {
		for _, name := range envNameInText.FindAllString(text, 32) {
			if len(names) >= 8 {
				break
			}
			rememberAuthCredential(&names, seen, name)
		}
	}
	return names
}

func rememberAuthCredential(names *[]string, seen map[string]bool, name string) {
	if name == "" || seen[name] || !secretName(name) || !credentialPattern.MatchString(name) {
		return
	}
	seen[name] = true
	*names = append(*names, name)
}

// credentialAlternativeGroups splits an either/or auth sentence into OR
// groups. "both A and B" is one group. A single name, or a list that is not
// phrased as alternatives, stays nil so every name is independently required.
func credentialAlternativeGroups(text string, names []string) [][]string {
	if len(names) < 2 {
		return nil
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "either") && !strings.Contains(lower, " or ") {
		return nil
	}
	known := map[string]bool{}
	for _, name := range names {
		known[name] = true
	}
	paired := map[string]bool{}
	seen := map[string]bool{}
	var groups [][]string
	for _, match := range bothCredentialPair.FindAllStringSubmatch(text, 8) {
		key := match[1] + "\x00" + match[2]
		if seen[key] || !known[match[1]] || !known[match[2]] {
			continue
		}
		seen[key] = true
		groups = append(groups, []string{match[1], match[2]})
		paired[match[1]], paired[match[2]] = true, true
	}
	for _, name := range names {
		if !paired[name] {
			groups = append(groups, []string{name})
		}
	}
	if len(groups) < 2 {
		return nil
	}
	return groups
}

// errEmptyToolList marks a probe where the server answered tools/list with a
// valid response carrying zero tools — distinct from a process that died or
// never answered, so a reviewer may treat answered-empty as a credential or
// configuration gate.
var errEmptyToolList = errors.New("MCP tools/list answered with zero tools")

func credentialGateFrom(imported ImportedArtifact, err error) (ImportedArtifact, *CredentialGate, bool) {
	if err == nil {
		return ImportedArtifact{}, nil, false
	}
	names := authErrorCredentialNames(err)
	emptyTools := false
	if len(names) == 0 {
		names = emptyToolsCredentialNames(imported, err)
		emptyTools = len(names) > 0
	}
	if len(names) == 0 {
		return ImportedArtifact{}, nil, false
	}
	for _, name := range names {
		if !credentialNameDeclared(imported.Definition.Credentials, name) {
			imported.Definition.Credentials = append(imported.Definition.Credentials, CredentialInput{Name: name, Required: true})
		}
	}
	named := map[string]bool{}
	for _, name := range names {
		named[name] = true
	}
	for index := range imported.Definition.Credentials {
		input := &imported.Definition.Credentials[index]
		if named[input.Name] && secretName(input.Name) {
			input.Required = true
		}
	}
	// Placeholder import tools are not a tools/list. Drop them so a caller that
	// only looks at the packet cannot register a contract the server never spoke.
	imported.Definition.Tools = nil
	imported.Definition.Source.ToolContractDigest = ""
	imported.Definition.Source.ToolContractSource = ""
	// Either/or alternatives stay on the definition so runtime admission asks
	// for one complete group instead of every required input at once. Members
	// that did not end up required (non-secret names) cannot satisfy a group.
	required := map[string]bool{}
	for _, input := range imported.Definition.Credentials {
		if input.Required {
			required[input.Name] = true
		}
	}
	var groups [][]string
	for _, group := range credentialAlternativeGroups(err.Error(), names) {
		var members []string
		for _, name := range group {
			if required[name] {
				members = append(members, name)
			}
		}
		if len(members) > 0 {
			groups = append(groups, members)
		}
	}
	if len(groups) < 2 {
		groups = nil
	}
	if groups == nil && emptyTools {
		groups = emptyToolsCredentialGroups(imported.Definition.Credentials)
	}
	imported.Definition.CredentialGroups = groups
	return imported, &CredentialGate{
		Names:  names,
		Groups: groups,
		Detail: publicPrepareError(err),
	}, true
}

// emptyToolsCredentialNames promotes a "tools/list answered with zero tools"
// probe failure into a credential gate when the definition already declares
// required inputs. Servers such as mcp-atlassian register no tools until
// their connection config exists; without this path the review would fail
// outright instead of asking for the declared credentials. A process that
// died without answering (no errEmptyToolList) or a definition with no
// required inputs still fails the review.
func emptyToolsCredentialNames(imported ImportedArtifact, err error) []string {
	if err == nil || !errors.Is(err, errEmptyToolList) {
		return nil
	}
	var names []string
	for _, input := range imported.Definition.Credentials {
		if input.Required {
			names = append(names, input.Name)
		}
	}
	slices.Sort(names)
	return names
}

// emptyToolsCredentialGroups groups required inputs by their PREFIX_
// namespace so a server that accepts either of two product configurations
// (JIRA_* or CONFLUENCE_*) asks for one complete set instead of every input
// at once. A prefix becomes an alternative only when it covers at least two
// inputs and one of them is a secret; otherwise inputs stay independently
// required.
func emptyToolsCredentialGroups(inputs []CredentialInput) [][]string {
	var order []string
	buckets := map[string][]string{}
	for _, input := range inputs {
		if !input.Required {
			continue
		}
		prefix, _, _ := strings.Cut(input.Name, "_")
		if prefix == "" {
			continue
		}
		if _, ok := buckets[prefix]; !ok {
			order = append(order, prefix)
		}
		buckets[prefix] = append(buckets[prefix], input.Name)
	}
	var groups [][]string
	for _, prefix := range order {
		members := buckets[prefix]
		if len(members) < 2 || !slices.ContainsFunc(members, secretName) {
			continue
		}
		groups = append(groups, members)
	}
	if len(groups) < 2 {
		return nil
	}
	return groups
}

func credentialNameDeclared(inputs []CredentialInput, name string) bool {
	for _, input := range inputs {
		if input.Name == name {
			return true
		}
	}
	return false
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

// preflightCommandArgs overrides a network transport with stdio. The probe
// writes MCP on stdin; an image command such as "--transport sse" never reads
// that stream, so tools/list waits until the deadline.
func preflightCommandArgs(imported ImportedArtifact) []string {
	argv := importedCommandArgs(imported)
	rewritten := stdioTransportArgs(argv)
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
	return networkTransportValue(importedCommandArgs(imported))
}

func networkTransportValue(argv []string) string {
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

// stdioTransportArgs rewrites a network transport flag to stdio. The companion
// always speaks MCP on the child's stdio, so the same override used for
// preflight probes applies to the runtime launch as well.
func stdioTransportArgs(argv []string) []string {
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

func preflightDockerRunArgs(imported ImportedArtifact, name string, runFlags []string, commandArgs []string, extra ...string) []string {
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
	args = append(args, runtimeEnvironmentArgs(imported.Definition)...)
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

type probeOptions struct {
	values       map[string]string
	allowNetwork bool
}

type probeOptionsKey struct{}

func withProbeOptions(ctx context.Context, opts probeOptions) context.Context {
	return context.WithValue(ctx, probeOptionsKey{}, opts)
}

func probeOptionsFrom(ctx context.Context) probeOptions {
	if ctx == nil {
		return probeOptions{}
	}
	opts, _ := ctx.Value(probeOptionsKey{}).(probeOptions)
	return opts
}

func withoutNetworkNone(args []string) []string {
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

func preflightCredentialArgs(ctx context.Context, imported ImportedArtifact) ([]string, func(), error) {
	prepared, _, err := preparedForSource(ArtifactSource{Repository: imported.Definition.Source.Repository, CommitSHA: imported.Definition.Source.CommitSHA, Subfolder: imported.Definition.Source.Subfolder})
	if err != nil {
		return nil, func() {}, err
	}
	root := os.Getenv("HUB_STATE")
	directory, err := os.MkdirTemp(root, "hermes-preflight-credentials-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	args := []string{}
	if prepared.StateTarget != "" {
		args = append(args, "--tmpfs", prepared.StateTarget+":rw,nosuid,nodev,uid=10001,gid=10001,mode=0700,size=16m")
	}
	if opts := probeOptionsFrom(ctx); opts.values != nil {
		// Owner-submitted secrets stay in a 0600 env file, not in process argv
		// and not in the error text. The placeholder "preflight" is only for
		// the unauthenticated probe that has no real value yet.
		var lines []string
		for _, input := range imported.Definition.Credentials {
			value := strings.TrimSpace(opts.values[input.Name])
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
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
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
		if body, ok := prepared.PreflightFiles[input.Name]; ok {
			path := directory + string(os.PathSeparator) + input.Name + ".json"
			if err := os.WriteFile(path, body, 0600); err != nil {
				cleanup()
				return nil, func() {}, err
			}
			target := "/run/hermes-preflight/" + input.Name + ".json"
			args = append(args, "--mount", "type=bind,source="+dockerBindSource(path)+",target="+target+",readonly")
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

// admitWithSubmittedCredentials runs tools/list with the owner's submitted
// secrets and, only for this probe, with network egress. A confirmed tool
// list is the only result that may be registered. The placeholder value
// "preflight" is not used here.
func admitWithSubmittedCredentials(ctx context.Context, artifactsDir string, definition ToolDefinition, secrets map[string]string) (ToolDefinition, error) {
	if definition.Source.ArchiveDigest != "" {
		if _, err := storedArtifactLoader(ctx, artifactsDir, definition.Source.ArchiveDigest, definition.Source.Image, 8<<30); err != nil {
			return ToolDefinition{}, err
		}
	}
	imported := ImportedArtifact{Definition: definition}
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probeCtx = withProbeOptions(probeCtx, probeOptions{values: secrets, allowNetwork: true})
	list := localMCPToolList
	if imported.Definition.Source.NetworkTransport != "" {
		list = networkMCPToolList
	}
	tools, err := list(probeCtx, imported)
	if err != nil {
		return ToolDefinition{}, err
	}
	if _, err := ApplyPreflightToolList(&imported, tools); err != nil {
		return ToolDefinition{}, err
	}
	return imported.Definition, nil
}
