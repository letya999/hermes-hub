package api

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestValidateLocalImageName(t *testing.T) {
	for _, image := range []string{"hermes-artifact/serena", "local/mcp:dev"} {
		if err := ValidateLocalImageName(image); err != nil {
			t.Fatalf("%s rejected: %v", image, err)
		}
	}
	for _, image := range []string{"", "repo@sha256:" + repeatHex('a'), "../escape", "repo//mcp", "repo mcp", "/absolute"} {
		if err := ValidateLocalImageName(image); err == nil {
			t.Fatalf("unsafe image %q accepted", image)
		}
	}
}

func TestLoadStoredOCIArtifactFailsBeforeDockerForUnsafeInputs(t *testing.T) {
	if _, err := LoadStoredOCIArtifact(context.Background(), t.TempDir(), "sha256:"+repeatHex('a'), "repo@sha256:"+repeatHex('b'), 1<<20); err == nil {
		t.Fatal("unsafe image reached Docker")
	}
	if _, err := LoadStoredOCIArtifactReader(context.Background(), nil, "repo/mcp"); err == nil {
		t.Fatal("nil artifact accepted")
	}
	if _, err := LoadStoredOCIArtifactWith(context.Background(), nil, "repo/mcp", nil); err == nil {
		t.Fatal("nil loader inputs accepted")
	}
	if _, err := LoadStoredOCIArtifact(context.Background(), t.TempDir(), "bad", "repo/mcp", 1<<20); err == nil {
		t.Fatal("malformed archive digest accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunArtifactDocker(ctx, t.TempDir(), nil, "version"); err == nil {
		t.Fatal("cancelled Docker command succeeded")
	}
}

func TestLoadStoredOCIArtifactWithEnforcesLocalRuntimeAndTagsDigest(t *testing.T) {
	const imageID = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var calls []string
	run := func(_ context.Context, input io.Reader, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		switch args[0] {
		case "context":
			return []byte("unix:///var/run/docker.sock\n"), nil
		case "info":
			return []byte("linux\n"), nil
		case "load":
			data, _ := io.ReadAll(input)
			if !bytes.Equal(data, []byte("oci")) {
				t.Fatal("artifact stream was not passed to docker load")
			}
			return []byte("Loaded image ID: " + imageID), nil
		case "image":
			return []byte(imageID + "\n"), nil
		default:
			return nil, nil
		}
	}
	got, err := LoadStoredOCIArtifactWith(context.Background(), bytes.NewReader([]byte("oci")), "hermes/mcp", run)
	if err != nil || got != imageID {
		t.Fatalf("image load got %q err=%v", got, err)
	}
	if len(calls) != 5 || calls[0] != "context" || calls[1] != "info" || calls[2] != "load" || calls[3] != "tag" || calls[4] != "image" {
		t.Fatalf("unexpected docker calls: %v", calls)
	}
}
