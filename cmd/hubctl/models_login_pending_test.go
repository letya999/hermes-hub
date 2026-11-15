package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
)

// A request the owner has not yet submitted reports its reviewed link and
// leaves generated/model-grants.json unwritten.
func TestModelsLoginReportsPendingRequests(t *testing.T) {
	dir := t.TempDir()
	writeModelsLoginDocs(t, dir)
	broker := &fakeModelsBroker{requests: map[string]brokerv1.Request{
		"cliproxy-session": {ID: "req-1", Status: "pending", AuthorizationURL: "https://broker/connect/req-1"},
		"model-api-key":    {ID: "req-2", Status: "ready", CredentialID: "cred-key"},
	}}
	report, err := modelsLogin(context.Background(), dir, "google-pro-1", broker, broker)
	if err != nil {
		t.Fatal(err)
	}
	if report.Session.AuthorizationURL == "" || report.ProxyAccess.Init.GrantID != "g-key" || report.ProxyAccess.Relay.GrantID != "g-key" {
		t.Fatalf("pending link missing: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated", "model-grants.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no grants map until every credential is granted")
	}
}
