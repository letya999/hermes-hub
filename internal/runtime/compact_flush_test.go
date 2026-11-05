package runtime

import (
	"testing"
)

// SPEC-0085 R2.6: the compact flush is wired only when the vault is mounted —
// without HUB_VAULT the option stays nil and compact runs unhooked.
func TestCompactFlushRequiresVault(t *testing.T) {
	t.Setenv("HUB_VAULT", "")
	if compactFlush("s") != nil {
		t.Fatal("flush must be nil without a vault mount")
	}
}
