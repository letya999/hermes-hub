package toolhub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRestrictedBuildKitProfile(t *testing.T) {
	profile := filepath.Join("..", "..", "docker", "seccomp-buildkit-rootless.json")
	abs, err := filepath.Abs(profile)
	if err != nil {
		t.Fatal(err)
	}
	args, err := restrictedBuildKitCreateArgs("hermes-buildkit", "hermes-buildkit-state", abs, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"moby/buildkit@sha256:93efd7f1c17f16cea080c34f68e19863b9fe541db550bf607221ce425c0ab9ef",
		"none", "ALL", "SETUID", "SETGID", "2g", "512", "--oci-worker-no-process-sandbox", "--oci-worker-snapshotter=native",
	} {
		if !slices.Contains(args, required) {
			t.Fatalf("builder profile omitted %q: %v", required, args)
		}
	}
	for _, forbidden := range []string{"--privileged", "host", "/var/run/docker.sock"} {
		if slices.Contains(args, forbidden) {
			t.Fatalf("builder profile permits %q", forbidden)
		}
	}
	if TrustedBuilderPolicy != "rootless-buildkit-bootstrap-v1" || !builderArgsAreTrustedBootstrap(args) {
		t.Fatal("builder policy is not the explicit trusted bootstrap")
	}
}

func TestRestrictedBuildProxyIsDenyByDefault(t *testing.T) {
	config := restrictedBuildProxyConfig()
	for _, host := range restrictedBuildHosts {
		if !strings.Contains(config, host) {
			t.Fatalf("package host %q omitted", host)
		}
	}
	for _, forbidden := range []string{"allow all", "allow localhost", "allow manager", "port 80", "access_log stdio"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("unsafe proxy rule %q", forbidden)
		}
	}
	if !strings.Contains(config, "http_access deny all") || !strings.Contains(config, "acl tlsport port 443") {
		t.Fatal("proxy is not deny-by-default TLS-only")
	}
}

func TestRestrictedBuilderDependenciesAreImmutable(t *testing.T) {
	for _, image := range []string{restrictedBuildKitImage, restrictedBuildProxyImage, restrictedSBOMScannerImage} {
		parts := strings.Split(image, "@sha256:")
		if len(parts) != 2 || len(parts[1]) != 64 {
			t.Fatalf("mutable builder dependency %q", image)
		}
	}
}

func TestRestrictedBuildKitProfileRejectsDrift(t *testing.T) {
	directory := t.TempDir()
	bad := filepath.Join(directory, "seccomp.json")
	if err := os.WriteFile(bad, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restrictedBuildKitCreateArgs("hermes-buildkit", "hermes-buildkit-state", bad, "", "", "", ""); err == nil {
		t.Fatal("modified seccomp profile accepted")
	}
	if _, err := restrictedBuildKitCreateArgs("bad name", "hermes-buildkit-state", bad, "", "", "", ""); err == nil {
		t.Fatal("unsafe builder identity accepted")
	}
	profile, err := os.ReadFile(filepath.Join("..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(profile)) != restrictedBuildKitSeccompSHA256 {
		t.Fatal("repository seccomp profile drifted")
	}
}

func TestRestrictedBuildNetworkTopology(t *testing.T) {
	profile, err := filepath.Abs(filepath.Join("..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	builder, err := restrictedBuildKitCreateArgs("hermes-buildkit", "hermes-buildkit-state", profile, "hermes-build", "hermes-build-proxy", "hermes-build-context", "hermes-build-output")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(builder, "\x00")
	for _, expected := range []string{"--network\x00hermes-build", "HTTP_PROXY=http://hermes-build-proxy:3128", "HTTPS_PROXY=http://hermes-build-proxy:3128", "NO_PROXY=", "http_proxy=http://hermes-build-proxy:3128", "https_proxy=http://hermes-build-proxy:3128", "no_proxy=", "type=volume,source=hermes-build-context,target=/run/hermes-context,readonly", "type=volume,source=hermes-build-output,target=/run/hermes-output"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("builder proxy topology omitted %q", expected)
		}
	}
	proxy, err := restrictedBuildProxyCreateArgs("hermes-build-proxy", "hermes-build", "hermes-build-config")
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(proxy, "\x00")
	for _, expected := range []string{restrictedBuildProxyImage, "--read-only", "--cap-drop\x00ALL", "no-new-privileges", "256m", "64", "type=volume,source=hermes-build-config,target=/run/hermes-config,readonly"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("proxy profile omitted %q", expected)
		}
	}
	if _, err := restrictedBuildKitCreateArgs("hermes-buildkit", "hermes-buildkit-state", profile, "hermes-build", "", "", ""); err == nil {
		t.Fatal("networked builder without proxy accepted")
	}
	if _, err := restrictedBuildKitCreateArgs("hermes-buildkit", "hermes-buildkit-state", profile, "hermes-build", "hermes-build-proxy", "hermes-build-context", ""); err == nil {
		t.Fatal("builder output without isolated volume accepted")
	}
}

func TestRestrictedBuildPipelinePublishesOnlyVerifiedOCI(t *testing.T) {
	base := "example/runtime@sha256:" + strings.Repeat("a", 64)
	recipe, buildContext := recipeFixture(t, "FROM "+base+"\nCOPY . /app\nUSER 10001:10001\nENTRYPOINT [\"/app/server\"]\n")
	archive, _ := ociFixture(t, "legacy-empty-subject")
	store := t.TempDir()
	seccomp, err := filepath.Abs(filepath.Join("..", "..", "docker", "seccomp-buildkit-rootless.json"))
	if err != nil {
		t.Fatal(err)
	}
	var network string
	run := func(_ context.Context, input io.Reader, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("missing docker arguments")
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	artifact, err := buildRestrictedOCI(ctx, recipe, buildContext, RestrictedBuildConfig{SeccompPath: seccomp, ArtifactDirectory: store, MaxArtifactBytes: 1 << 20}, run)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.ArchiveDigest == "" || artifact.Evidence.ProvenanceDigest == "" || artifact.Evidence.SBOMDigest == "" {
		t.Fatalf("missing verified evidence: %+v", artifact)
	}
	if files, _ := os.ReadDir(store); len(files) != 1 {
		t.Fatalf("unexpected quarantine objects: %v", files)
	}
}

func TestRestrictedBuildPipelineFailsClosed(t *testing.T) {
	base := "example/runtime@sha256:" + strings.Repeat("a", 64)
	recipe, buildContext := recipeFixture(t, "FROM "+base+"\nCOPY . /app\nUSER 10001:10001\nENTRYPOINT [\"/app/server\"]\n")
	if _, err := BuildRestrictedOCI(context.Background(), recipe, buildContext, RestrictedBuildConfig{}); err == nil {
		t.Fatal("unbounded builder configuration accepted")
	}
	if _, err := buildRestrictedOCI(context.Background(), recipe, buildContext, RestrictedBuildConfig{}, nil); err == nil {
		t.Fatal("missing Docker boundary accepted")
	}
}
