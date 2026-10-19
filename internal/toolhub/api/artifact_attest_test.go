package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// buildctl rejects attestation modes it does not know; provenance mode is
// lowercase "max", anything else fails the solve after the whole build ran.
func TestRestrictedBuildAttestationModeIsLowercase(t *testing.T) {
	base := "example/runtime@sha256:" + strings.Repeat("a", 64)
	recipe, buildContext := recipeFixture(t, "FROM "+base+"\nCOPY . /app\nUSER 10001:10001\nENTRYPOINT [\"/app/server\"]\n")
	archive, _ := ociFixture(t, "legacy-empty-subject")
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	var network string
	var buildArgs []string
	run := func(_ context.Context, input io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "context":
			return []byte("npipe:////./pipe/docker_engine\n"), nil
		case "info":
			return []byte("linux\n"), nil
		case "network":
			if len(args) > 2 && args[1] == "create" {
				network = args[len(args)-1]
			}
			return []byte("ok"), nil
		case "inspect":
			if slices.Contains(args, "--format") {
				return []byte("172.30.0.2\n"), nil
			}
			profile := map[string]any{"Config": map[string]any{"User": "1000:1000"}, "HostConfig": map[string]any{"ReadonlyRootfs": true, "Privileged": false, "NanoCpus": 1, "Memory": 1, "PidsLimit": 1, "NetworkMode": network, "Binds": []string{}, "CapAdd": []string{"CAP_SETGID", "CAP_SETUID"}, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"seccomp=profile", "systempaths=unconfined", "no-new-privileges=true"}, "Mounts": []map[string]any{{"Type": "volume"}}}}
			body, _ := json.Marshal([]any{profile, profile})
			return body, nil
		case "exec":
			if slices.Contains(args, "nc") {
				request, _ := io.ReadAll(input)
				if bytes.Contains(request, []byte("example.com")) {
					return []byte("HTTP/1.1 403 Forbidden\r\n"), nil
				}
				return []byte("HTTP/1.1 200 Connection established\r\n"), nil
			}
			if slices.Contains(args, "build") {
				buildArgs = append([]string(nil), args...)
			}
			return []byte("ok"), nil
		case "cp":
			if args[1] == "-" {
				_, _ = io.Copy(io.Discard, input)
				return nil, nil
			}
			if err := os.WriteFile(args[len(args)-1], archive, 0600); err != nil {
				return nil, err
			}
		}
		return []byte("ok"), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := BuildRestrictedOCIWith(ctx, recipe, buildContext, RestrictedBuildConfig{SeccompPath: seccomp, ArtifactDirectory: t.TempDir(), MaxArtifactBytes: 1 << 20}, run); err != nil {
		t.Fatal(err)
	}
	if len(buildArgs) == 0 {
		t.Fatal("buildctl build was never invoked")
	}
	if slices.Contains(buildArgs, "attest:provenance=mode=Max") || !slices.Contains(buildArgs, "attest:provenance=mode=max") {
		t.Fatalf("provenance mode must be lowercase max: %v", buildArgs)
	}
}
