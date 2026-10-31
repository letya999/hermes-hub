package toolhub

import (
	"github.com/letya999/hermes-hub/internal/audit"
	"net/http"
	"path/filepath"
	"testing"
)

// Positive dispatch fixtures use the production DTO writer and a real private
// ledger. Negative missing-audit tests do not use these positive-only helpers.
func attachGatewayAudit(t testing.TB, g *Gateway) {
	t.Helper()
	if g.AuditWrite != nil {
		return
	}
	g.AuditWrite = testAuditWriter(t)
}

func endpointWithTestAudit(t *testing.T, config EndpointConfig, store *Store) (http.Handler, error) {
	t.Helper()
	t.Setenv("HUB_AUDIT_LEDGER", filepath.Join(t.TempDir(), "audit.jsonl"))
	return NewEndpointHandler(config, store)
}

func testAuditWriter(t testing.TB) func(string, map[string]string) error {
	t.Helper()
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return LedgerAuditWriter(ledger)
}
