package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Status token resolution walks flag > env > the infra space's auth files;
// the file fallback is the live path for compose deployments.
func TestResolveStatusToken(t *testing.T) {
	dir := t.TempDir()
	if got := resolveStatusToken(dir); got != "" {
		t.Fatalf("token without auth file: %q", got)
	}
	auth := filepath.Join(dir, "supervisor.auth")
	if err := os.WriteFile(auth, []byte("HUB_SUPERVISOR_AUTH=file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveStatusToken(dir); got != "file-token" {
		t.Fatalf("file fallback: %q", got)
	}
	t.Setenv("HUB_SUPERVISOR_AUTH", "env-token")
	if got := resolveStatusToken(dir); got != "env-token" {
		t.Fatalf("env precedence: %q", got)
	}
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-token")
	if got := resolveStatusToken(dir); got != "runtime-token" {
		t.Fatalf("runtime env: %q", got)
	}
}

func TestResolveGatewayToken(t *testing.T) {
	dir := t.TempDir()
	if got := resolveGatewayToken(dir, "dev", ""); got != "" {
		t.Fatalf("token without files: %q", got)
	}
	// The supervisor bearer doubles as the gateway control token in compose.
	if got := resolveGatewayToken(dir, "dev", "fallback-tok"); got != "fallback-tok" {
		t.Fatalf("supervisor fallback: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "communication.dev.env"), []byte("HUB_COMMUNICATION_AUTH=gw-env-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveGatewayToken(dir, "dev", ""); got != "gw-env-tok" {
		t.Fatalf("env file: %q", got)
	}
	t.Setenv("HUB_COMMUNICATION_AUTH", "direct-tok")
	if got := resolveGatewayToken(dir, "dev", "fallback-tok"); got != "direct-tok" {
		t.Fatalf("env precedence: %q", got)
	}
}

// `hubctl user` only accepts the disable verb and requires --dir; everything
// else is usage before any HTTP stage runs.
func TestRunUserUsage(t *testing.T) {
	if err := runUser(context.Background(), nil); err == nil {
		t.Fatal("no verb accepted")
	}
	if err := runUser(context.Background(), []string{"enable"}); err == nil {
		t.Fatal("unknown verb accepted")
	}
	if err := runUser(context.Background(), []string{"disable"}); err == nil {
		t.Fatal("disable without --dir accepted")
	}
	if err := runUser(context.Background(), []string{"disable", "--dir", t.TempDir(), "extra"}); err == nil {
		t.Fatal("positional arg accepted")
	}
}
