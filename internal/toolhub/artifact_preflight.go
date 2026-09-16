package toolhub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PreflightImportedArtifact loads the quarantine image into local Docker,
// runs tools/list with the MCP runtime profile (not the BuildKit bootstrap
// profile), and restamps the review packet with that confirmed contract.
func PreflightImportedArtifact(ctx context.Context, imported ImportedArtifact, artifactsDir string) (ImportedArtifact, ConfirmedToolContract, error) {
	if _, err := LoadStoredOCIArtifact(ctx, artifactsDir, imported.Artifact.ArchiveDigest, imported.Definition.Source.Image, 8<<30); err != nil {
		return ImportedArtifact{}, ConfirmedToolContract{}, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return restampFromMCPList(listCtx, imported, listMCPToolsFromLocalImage)
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
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	name := "hermes-preflight-" + hex.EncodeToString(nonce[:])
	args := preflightDockerRunArgs(imported, name)
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
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	enc := json.NewEncoder(stdin)
	for _, msg := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "hub-artifact-preflight", "version": "0.3.0"}}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
	} {
		if err := enc.Encode(msg); err != nil {
			return nil, fmt.Errorf("%w: MCP preflight write: %v: %s", ErrIsolation, err, stderr.String())
		}
	}
	if err := stdin.Close(); err != nil {
		return nil, err
	}
	tools, err := decodeMCPToolList(stdout)
	if err != nil {
		return nil, fmt.Errorf("%w: MCP tools/list: %v: %s", ErrIsolation, err, stderr.String())
	}
	return tools, nil
}

func decodeMCPToolList(r io.Reader) ([]ToolSpec, error) {
	dec := json.NewDecoder(r)
	for {
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := dec.Decode(&envelope); err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("%w: MCP tools/list empty", ErrIsolation)
			}
			return nil, err
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("%w: MCP error %s", ErrIsolation, envelope.Error.Message)
		}
		if strings.TrimSpace(string(envelope.ID)) != "2" {
			continue
		}
		var listed struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(envelope.Result, &listed); err != nil || len(listed.Tools) == 0 {
			return nil, fmt.Errorf("%w: MCP tools/list empty: %v", ErrIsolation, err)
		}
		tools := make([]ToolSpec, 0, len(listed.Tools))
		for _, tool := range listed.Tools {
			name := strings.TrimSpace(tool.Name)
			if !toolNamePattern.MatchString(name) {
				return nil, fmt.Errorf("%w: MCP tool %q is not a catalog name", ErrInvalid, name)
			}
			tools = append(tools, ToolSpec{Name: name, Effect: ReadEffect})
		}
		return tools, nil
	}
}

func preflightDockerRunArgs(imported ImportedArtifact, name string) []string {
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
	args = append(args, imported.Definition.Source.Image)
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
