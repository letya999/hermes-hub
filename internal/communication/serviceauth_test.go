package communication

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tokens file with one malformed entry must not lend trust to the valid
// entries: a bad writer could otherwise pick which principals each reader
// sees. The whole file is rejected fail-closed, matching ToolHub's loader.
func TestToolHubCredentialRejectsPoisonedTokensFile(t *testing.T) {
	g := &Gateway{}
	g.config.ControlTokensFile = filepath.Join(t.TempDir(), "tokens.json")
	valid := `"` + strings.Repeat("a", 32) + `":{"identity_schema":1,"principal_id":"alice","external_identity_id":"telegram-11","context_id":"alice","runtime_id":"alice","conversation_id":"telegram-11","delivery_target_id":"telegram-11","policy_version":"policy-1"}`
	poisoned := `{"` + strings.Repeat("b", 32) + `":{"identity_schema":1,"principal_id":"alice"},` + valid + `}`
	if err := os.WriteFile(g.config.ControlTokensFile, []byte(poisoned), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, _ := g.toolHubCredential("alice"); token != "" {
		t.Fatal("poisoned tokens file still lent a credential")
	}
}
