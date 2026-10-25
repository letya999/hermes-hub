package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The operator team surface publishes a confirmed Team record, mints the
// team-owned shared connection and drops it again — the same confirm gate
// and monotonic-revision rules a capability policy obeys.
func TestCapabilityCLITeamLifecycle(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")
	// Seed a shareable definition so team-share has a target.
	store := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "team-gitlab", Version: "1.0.0",
		Transport: api.RemoteMCP,
		Source:    api.DefinitionSource{URL: "https://gitlab.example.com/mcp", TLSMode: "required"},
		Tools:     []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Shareable: "credential", Rationale: "test"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 4096, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"gitlab.example.com"}},
		Health:    api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	team := api.Team{Schema: api.SchemaVersion, TeamID: "eng", Members: []string{"alice", "bob"}, Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus}
	input := filepath.Join(dir, "team.json")
	body, _ := json.Marshal(team)
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--kind", "team", "--file", input, "--toolhub-store", storePath}
	if err := runCapability(args); err == nil {
		t.Fatal("unconfirmed team record committed")
	}
	if err := runCapability(append(args, "--confirm")); err != nil {
		t.Fatal(err)
	}
	loaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.TeamFor("eng"); !ok {
		t.Fatal("confirmed team not published")
	}
	// Share: the operator supplies the broker locator the team credential
	// lives under; one team-owned connection is minted.
	share := []string{"--kind", "team-share", "--toolhub-store", storePath,
		"--team", "eng", "--definition", "team-gitlab", "--version", "1.0.0",
		"--backend", "broker", "--locator", "op://vault/team-gitlab", "--keys", "TOKEN"}
	if err := runCapability(share); err != nil {
		t.Fatal(err)
	}
	loaded, err = toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	connectionID := api.DeterministicID("conn", "team", "eng", "team-gitlab", "1.0.0")
	hasConnection := func(s *toolhub.Store, id string) bool {
		for _, c := range s.Connections() {
			if c.ConnectionID == id {
				return true
			}
		}
		return false
	}
	if !hasConnection(loaded, connectionID) {
		t.Fatal("team-share minted no connection")
	}
	// A non-shareable definition cannot be shared.
	plain := definition
	plain.DefinitionID = "plain-mcp"
	plain.Workload.Shareable = ""
	if err := loaded.RegisterDefinition(plain); err != nil {
		t.Fatal(err)
	}
	badShare := append([]string{}, share...)
	badShare[len(badShare)-6] = "plain-mcp"
	if err := runCapability(badShare); err == nil {
		t.Fatal("non-shareable definition shared")
	}
	// Drop: the shared rows go away once.
	if err := runCapability([]string{"--kind", "team-drop", "--toolhub-store", storePath, "--connection-id", connectionID}); err != nil {
		t.Fatal(err)
	}
	loaded, err = toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if hasConnection(loaded, connectionID) {
		t.Fatal("team-drop left the shared connection")
	}
	if err := runCapability([]string{"--kind", "team-drop", "--toolhub-store", storePath, "--connection-id", connectionID}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("second drop: %v", err)
	}
}
