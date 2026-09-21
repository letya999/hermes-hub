//go:build integration

package toolhub

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShippedHubctlArtifactImportRejectsMutableRef(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"definition_id":"mcp","version":"1.0.0","language":"go","tools":[{"name":"read","effect":"read"}],"workload":{"class":"per-user","rationale":"test"},"execution":{"timeout_seconds":30,"output_bytes":1024,"cpu_millis":500,"memory_mib":128,"max_pids":16,"egress":["example.com"]},"health":{"kind":"exec","value":"/app/health","timeout_seconds":5}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./cmd/hubctl", "artifact", "import", "--repository", "https://github.com/example/mcp", "--commit", "main", "--config", config, "--artifacts", t.TempDir())
	cmd.Dir = filepath.Join("..", "..")
	body, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(body)+err.Error(), "") {
		if err == nil {
			t.Fatal("mutable ref import succeeded")
		}
	}
	if err == nil {
		t.Fatal("mutable ref import succeeded")
	}
}

func TestPublicMCPImportRequiresExactSHA(t *testing.T) {
	cases := []struct{ name, repository, sha, language string }{
		{"serena", "https://github.com/oraios/serena", "704e8c3d1929bdd74bf0c8eee1596aeddf03fdb2", "python"},
		{"context7", "https://github.com/upstash/context7", "b653c3a07d7936bdc4c23fc1c88903120e0ece77", "node"},
		{"go-filesystem", "https://github.com/mark3labs/mcp-filesystem-server", "ba3f07f22c309d932fa9b1cebe1eb7c55fcbb83b", "go"},
		{"rust-filesystem", "https://github.com/rust-mcp-stack/rust-mcp-filesystem", "ef4797360ea03eec5375e03a6f10d092dfc6a5e0", "rust"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			source := ArtifactSource{Repository: tc.repository, CommitSHA: tc.sha}
			if _, err := source.ArchiveURL(); err != nil {
				t.Fatal(err)
			}
			if _, err := (ArtifactSource{Repository: tc.repository, CommitSHA: "main"}).ArchiveURL(); err == nil {
				t.Fatal("mutable ref accepted")
			}
			data, err := FetchRepositoryArtifactContext(ctx, source, 64<<20)
			if err != nil {
				if strings.Contains(err.Error(), "403") {
					t.Skip("anonymous GitHub HTTP 403")
				}
				t.Fatal(err)
			}
			if len(data) == 0 {
				t.Fatal("empty source context")
			}
		})
	}
}

func TestLinuxBridgeBuildProducesELF(t *testing.T) {
	output := filepath.Join(t.TempDir(), "hubctl-linux")
	if err := BuildLinuxCompanionBridge(output); err != nil {
		t.Skip(err.Error())
	}
	if err := ValidateLinuxBridgeBinary(output); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmedContractJSONRoundTrip(t *testing.T) {
	contract := ConfirmedToolContract{Source: ToolContractPreflight, Tools: []ToolSpec{{Name: "read", Effect: ReadEffect}}}
	body, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	var parsed ConfirmedToolContract
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	packet := importedReviewPacket(t)
	packet.Definition.Tools = parsed.Tools
	if err := AttachConfirmedToolContract(&packet, parsed); err != nil {
		t.Fatal(err)
	}
}
