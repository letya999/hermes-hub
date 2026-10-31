package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPreparedCredentialGate(t *testing.T) {
	old := PreparedCatalogJSON
	defer func() { PreparedCatalogJSON = old }()
	commit := strings.Repeat("ab", 20)
	PreparedCatalogJSON = []byte(fmt.Sprintf(`[{
		"id": "prepared-x", "name": "X", "license": "MIT", "language": "node",
		"source": {"repository": "https://github.com/acme/gated", "commit_sha": "%s"},
		"contract_id": "github-pat", "contract_revision": 2,
		"entrypoint": ["server"], "read_tools": ["list"], "probe_tool": "list",
		"auth_before_tools_list": true,
		"connection": {"fields": [{"name": "API_TOKEN", "type": "secret", "required": true, "delivery": "env"}, {"name": "REGION", "type": "string", "delivery": "env"}]},
		"runbook": "r", "handoff": "h", "egress": ["api.example.io"]
	}]`, commit))

	// a prepared auth-before-list source returns a named credential gate
	src := ArtifactSource{Repository: "https://github.com/acme/gated", CommitSHA: commit}
	err := PreparedCredentialGate(src)
	var gate *CredentialGate
	if !errors.As(err, &gate) || len(gate.Names) != 1 || gate.Names[0] != "API_TOKEN" {
		t.Fatalf("gate: %v", err)
	}
	// unknown or unprepared sources pass through
	if err := PreparedCredentialGate(ArtifactSource{Repository: "https://github.com/acme/other", CommitSHA: commit}); err != nil {
		t.Fatalf("unprepared: %v", err)
	}
}
