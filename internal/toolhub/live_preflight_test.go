package toolhub

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveArtifactPreflight exercises the real docker-based tools/list probe
// against a locally loaded artifact image. Gated by HUB_LIVE_PREFLIGHT_IMAGE
// because it requires a Docker daemon and a pre-loaded artifact image.
func TestLiveArtifactPreflight(t *testing.T) {
	image := os.Getenv("HUB_LIVE_PREFLIGHT_IMAGE")
	if image == "" {
		t.Skip("set HUB_LIVE_PREFLIGHT_IMAGE to run the live preflight probe")
	}
	imported := ImportedArtifact{
		Definition: ToolDefinition{
			DefinitionID: "live-preflight",
			Version:      "0.0.1",
			Source:       DefinitionSource{Image: image, ArchiveDigest: "sha256:" + "0ab1c2d3e4f5061728394a5b6c7d8e9f0a1b2c3d4e5f60718293a4b5c6d7e8f9"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tools, err := listMCPToolsFromLocalImage(ctx, imported)
	if err == nil {
		t.Fatalf("expected auth error without credentials, got %d tools", len(tools))
	}
	t.Logf("first attempt error: %v", err)
	promoted, retry, retryErr := retryListWithPromotedCredentials(ctx, imported, err, listMCPToolsFromLocalImage)
	if retryErr != nil {
		t.Fatalf("promotion retry failed: %v", retryErr)
	}
	t.Logf("promoted credentials: %+v", promoted.Definition.Credentials)
	t.Logf("tools/list returned %d tools", len(retry))
	if len(retry) == 0 {
		t.Fatal("retry succeeded but returned no tools")
	}
}
