package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/fake"
)

// TestExplainGolden pins the explain output contract: the header carries
// the space and its effective digest, every leaf prints value plus the
// layer that wrote it, and fields stay sorted per document.
func TestExplainGolden(t *testing.T) {
	ctx := context.Background()
	client := spawnv1.NewInProc(&fake.Server{})
	tenant := spawnv1.Tenant{Org: "local", Owner: "alice"}
	bootstrapSpace(t, ctx, client, tenant, "alice")

	var out bytes.Buffer
	if err := runExplain(ctx, &out, []string{"alice"}, client); err != nil {
		t.Fatalf("explain: %v", err)
	}
	got := out.String()

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("output has %d lines", len(lines))
	}
	if !strings.HasPrefix(lines[0], "space alice effective sha256:") {
		t.Fatalf("header %q", lines[0])
	}
	// Every field line is doc.path = value [layer] — the fake attributes
	// each document to the layer that submitted it.
	checks := map[string]string{
		"config.runtime.timezone": `"Europe/Berlin" [user]`,
		"stack.identity.user":     `"alice" [platform-ceiling]`,
		"workspace.writable":      `["/workspace"] [org-template]`,
		"models.default":          `"everyday" [platform-template]`,
	}
	seen := map[string]bool{}
	for _, line := range lines[1:] {
		for want, suffix := range checks {
			if strings.HasPrefix(line, want+" = ") && strings.HasSuffix(line, suffix) {
				seen[want] = true
			}
		}
	}
	for want := range checks {
		if !seen[want] {
			t.Errorf("no field line for %s:\n%s", want, got)
		}
	}
	// Field order is deterministic: doc order, sorted paths inside each.
	docOrder := []string{"stack.", "config.", "models.", "workspace.", "tools.", "grants."}
	lastDoc := -1
	for _, line := range lines[1:] {
		for i, prefix := range docOrder {
			if strings.HasPrefix(line, prefix) {
				if i < lastDoc {
					t.Fatalf("docs out of order at %q", line)
				}
				lastDoc = i
			}
		}
	}
}

func TestExplainUsageAndTenant(t *testing.T) {
	ctx := context.Background()
	client := spawnv1.NewInProc(&fake.Server{})
	tenant := spawnv1.Tenant{Org: "local", Owner: "alice"}
	bootstrapSpace(t, ctx, client, tenant, "alice")

	var out bytes.Buffer
	if err := runExplain(ctx, &out, nil, client); err == nil {
		t.Fatal("missing space argument must fail")
	}
	// A foreign tenant must not read the space's provenance.
	if err := runExplain(ctx, &out, []string{"--owner", "bob", "alice"}, client); err == nil {
		t.Fatal("foreign tenant read the explain report")
	}
}
