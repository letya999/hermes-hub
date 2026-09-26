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
	"regexp"
	"strings"
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
	tools, err := localMCPToolList(listCtx, imported)
	if err != nil {
		promoted, retry, retryErr := retryListWithPromotedCredentials(listCtx, imported, err, localMCPToolList)
		if retryErr != nil {
			return ImportedArtifact{}, ConfirmedToolContract{}, fmt.Errorf("%v [credential-promotion retry: %v]", err, retryErr)
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
	tools, err := publishedMCPToolList(listCtx, imported)
	if err != nil {
		promoted, retry, retryErr := retryListWithPromotedCredentials(listCtx, imported, err, publishedMCPToolList)
		if retryErr != nil {
			return ImportedArtifact{}, ConfirmedToolContract{}, fmt.Errorf("%v [credential-promotion retry: %v]", err, retryErr)
		}
		imported, tools = promoted, retry
	}
	return restampFromMCPList(listCtx, imported, func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return tools, nil })
}

var publishedImagePull = pullPublishedImage
var publishedMCPToolList = listMCPToolsFromLocalImage
var storedArtifactLoader = LoadStoredOCIArtifact
var localMCPToolList = listMCPToolsFromLocalImage

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

// authErrorCredentialNames extracts secret-shaped env names a failed server
// printed in its auth error. Only TOKEN/SECRET/KEY-style names are promoted:
// plain config variables mentioned in usage text are not credentials.
func authErrorCredentialNames(err error) []string {
	if err == nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, match := range authErrorEnvVar.FindAllStringSubmatch(err.Error(), 8) {
		name := match[1]
		if name == "" {
			name = match[2]
		}
		if name == "" || seen[name] || !secretName(name) || !credentialPattern.MatchString(name) {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
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
	args := append(preflightDockerRunArgs(imported, name), "--help")
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
	credentialArgs, cleanup, err := preflightCredentialArgs(imported)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	args := preflightDockerRunArgs(imported, name, credentialArgs...)
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- image name is catalog-validated; flags are fixed.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_CONFIG=" + dockerConfig}
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
	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
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
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}
		if len(envelope.ID) == 0 && len(envelope.Result) == 0 {
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
			return nil, fmt.Errorf("%w: MCP tools/list empty: %v", ErrIsolation, err)
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
		return nil, fmt.Errorf("%w: MCP tools/list stream: %v", ErrIsolation, err)
	}
	return nil, fmt.Errorf("%w: MCP tools/list empty", ErrIsolation)
}

func preflightDockerRunArgs(imported ImportedArtifact, name string, extra ...string) []string {
	args := []string{
		"run", "--rm", "-i",
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
	}
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

func preflightCredentialArgs(imported ImportedArtifact) ([]string, func(), error) {
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
	return args, cleanup, nil
}
