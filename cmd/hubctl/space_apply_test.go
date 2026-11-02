package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
)

// space apply --confirm must re-render the environment before reconcile
// acts: a newly declared per-space instance only exists in the generated
// compose file once the published revision is rendered — acting on the
// previous render hits "no such service".
func TestSpaceApplyRendersBeforeReconcile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "dana")
	for _, sub := range []string{"config", "generated/passports"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("schema: 3\ninfra: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scope.yaml"), []byte("kind: user\nid: dana\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.dev.env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("# persona\n"), 0600); err != nil {
		t.Fatal(err)
	}
	head := "apiVersion: six-passport/v2\nmetadata: {space: dana, environment: dev, revision: r-0123456789abcdef, generated_at: 2026-11-02T00:00:00Z}\n"
	rev := passport.Revision{ID: "r-0123456789abcdef", Docs: map[string][]byte{
		"stack":     []byte(head + "kind: stack\nidentity: {user: dana}\nservices:\n    cliproxy-dana: {mode: per-space}\n    hermes-runtime: {mode: supervised}\n"),
		"config":    []byte(head + "kind: config\nruntime: {timezone: UTC}\nports: {oauth-callback: 8008, browser-vnc: 6081}\n"),
		"models":    []byte(head + "kind: models\ndefault: upstream\nroutes: {default: {ladder: [upstream]}}\nservices:\n    upstream: {via: openai-compatible, endpoint: http://model-relay:8318/v1}\n"),
		"workspace": []byte(head + "kind: workspace\nwritable: [/workspace]\nread_only: [/config]\n"),
		"tools":     []byte(head + "kind: tools\ncore:\n  discovery: {via: toolhub}\n  files: {via: toolhub}\n"),
		"grants":    []byte(head + "kind: grants\nallow:\n  workspace: [read]\n"),
	}}
	handlers := reconcile.Handlers{
		Rebuild:  func(string) error { return nil },
		Recreate: func(string) error { return nil },
		Respawn:  func() error { return nil },
		Live:     map[string]func() error{},
	}
	_, err := applyConfirm(context.Background(), dir, root, "dev", rev, "", nil, handlers)
	if err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile(filepath.Join(dir, "generated", "compose.dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "cliproxy-dana") {
		t.Fatal("reconcile acted on a stale render: new instance missing from generated compose")
	}
}

// A removal action must sweep compose orphans — the handler has to run
// `up --remove-orphans` so a dropped stack.services instance's container
// actually disappears (imperative topology, both directions).
func TestApplyRemoveHandlerSweepsOrphans(t *testing.T) {
	var captured []string
	old := dockerCompose
	dockerCompose = func(ctx context.Context, dir string, args ...string) error {
		captured = append([]string(nil), args...)
		return nil
	}
	defer func() { dockerCompose = old }()

	h := applyHandlers(context.Background(), t.TempDir(), "dev")
	if h.Remove == nil {
		t.Fatal("remove action has no handler")
	}
	if err := h.Remove(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--remove-orphans") {
		t.Fatalf("remove handler must sweep orphans, ran: %s", joined)
	}
}
