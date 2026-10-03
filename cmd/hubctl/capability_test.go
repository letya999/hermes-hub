package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestCapabilityCLIProtectedPublicationAndRevoke(t *testing.T) {
	dir := t.TempDir()
	storePath, input := filepath.Join(dir, "store.json"), filepath.Join(dir, "reviewed.json")
	write := func(value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := toolhub.CapabilityPolicy{Schema: toolhub.SchemaVersion, PolicyID: "personal", Members: []string{"alice"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Explicit empty starting policy", Status: toolhub.ActiveStatus}
	write(policy)
	args := []string{"capability", "--kind", "policy", "--file", input, "--toolhub-store", storePath}
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	profile := toolhub.CapabilityProfile{Schema: toolhub.SchemaVersion, ProfileID: "alice-default", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Environment: "dev", Generation: 1, PolicyVersion: "policy-1", PolicyID: "personal", PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Explicit empty profile", Status: toolhub.ActiveStatus}
	write(profile)
	args[2] = "profile"
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	auth.CapabilityProfile, auth.Environment, auth.Generation = profile.ProfileID, "dev", 1
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("empty published profile: %v %v", tools, err)
	}
	policy.Revision++
	policy.Status = toolhub.RevokedStatus
	write(policy)
	args[2] = "policy"
	if err := run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProjectedTools(auth); !errors.Is(err, toolhub.ErrUnauthorized) {
		t.Fatalf("existing reader retained revoked policy: %v", err)
	}
	policy.Revision = 1
	policy.Status = toolhub.ActiveStatus
	write(policy)
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), args); !errors.Is(err, toolhub.ErrConflict) {
		t.Fatalf("stale operator input accepted: %v", err)
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected input changed registry/history")
	}
}

func TestCapabilityCLIRejectsIncompleteAndMalformedRecords(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_STORE", "")
	for _, args := range [][]string{nil, {"--unknown"}, {"--kind", "grant"}, {"--kind", "policy", "--file", "x", "--toolhub-store", "x", "extra"}} {
		if err := runCapability(args); err == nil {
			t.Fatalf("bad flags: %v", args)
		}
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "record.json")
	storePath := filepath.Join(dir, "store.json")
	args := []string{"--kind", "policy", "--file", input, "--toolhub-store", storePath}
	if err := runCapability(args); err == nil {
		t.Fatal("missing input accepted")
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, `{}`, strings.Repeat(" ", (4<<20)+1)} {
		if err := os.WriteFile(input, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runCapability(args); err == nil {
			t.Fatal("bad input accepted")
		}
		if _, err := os.Stat(storePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("bad input created registry")
		}
	}
	args[1] = "profile"
	if err := os.WriteFile(input, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability(args); err == nil {
		t.Fatal("bad profile accepted")
	}
	if err := os.WriteFile(storePath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCapability(args); err == nil {
		t.Fatal("malformed protected store replaced")
	}
}
