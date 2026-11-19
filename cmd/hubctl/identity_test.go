package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/iam"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"gopkg.in/yaml.v3"
)

func identityMigrationFixture(t *testing.T, telegramIDs ...int64) ([]string, string, string) {
	t.Helper()
	dir := t.TempDir()
	config := communication.Config{OrganizationID: "personal", SpoolDir: filepath.Join(dir, "spool"), OIDCIssuer: "https://issuer.example", Users: []communication.User{{ID: "alice", Enabled: true, TelegramIDs: telegramIDs, StateDir: filepath.Join(dir, "state"), WorkspaceDir: filepath.Join(dir, "workspace")}}}
	body, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "communication.yaml")
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	links, _ := json.Marshal([]iam.Link{{Issuer: config.OIDCIssuer, Subject: "fixture-subject", Principal: "alice", TransportID: "slack-t1-u1"}})
	linkPath := filepath.Join(dir, "iam-links.json")
	if err := os.WriteFile(linkPath, links, 0600); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(dir, "control")
	return []string{"migrate", "--config", source, "--oidc-links", linkPath, "--control-dir", control, "--env", "dev", "--import-id", "fixture"}, control, source
}

func TestIdentityMigrationIsExplicitIsolatedAndPreservesSource(t *testing.T) {
	args, control, source := identityMigrationFixture(t, 7)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runIdentity(context.Background(), &out, args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(control); !os.IsNotExist(err) {
		t.Fatal("preview created control state")
	}
	for range 2 {
		out.Reset()
		if err := runIdentity(context.Background(), &out, append(args, "--apply")); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "alice") || strings.Contains(out.String(), "fixture-subject") || strings.Contains(out.String(), "t1/u1") {
			t.Fatal("migration output exposed identities")
		}
	}
	path, _ := identityhub.StorePath(control, "dev")
	r, err := identityhub.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, channel := range []contract.ChannelIdentity{{Channel: contract.Telegram, ID: "7"}, {Channel: contract.Slack, ID: "t1/u1"}} {
		p, err := r.Resolve(context.Background(), channel)
		if err != nil || p.ID != "alice" {
			t.Fatal("legacy mapping was not preserved")
		}
	}
	prod, _ := identityhub.StorePath(control, "prod")
	if _, err := os.Stat(prod); !os.IsNotExist(err) {
		t.Fatal("dev migration touched prod registry")
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source was rewritten")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(source), "iam-links-audit.jsonl")); !os.IsNotExist(err) {
		t.Fatal("migration opened legacy link writer")
	}
}

func TestIdentityMigrationRejectsUnknownFieldsWithoutDiagnosticLeak(t *testing.T) {
	args, control, source := identityMigrationFixture(t, 7)
	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("credential-key-marker: secret-value-marker\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	var out bytes.Buffer
	err = runIdentity(context.Background(), &out, append(args, "--apply"))
	if err == nil {
		t.Fatal("unexpected input fields accepted")
	}
	if strings.Contains(err.Error()+out.String(), "marker") {
		t.Fatal("parser echoed untrusted fields")
	}
	if _, err := os.Stat(control); !os.IsNotExist(err) {
		t.Fatal("invalid source created registry")
	}
}

func TestIdentityMigrationRefusesSymlinkSource(t *testing.T) {
	args, control, source := identityMigrationFixture(t, 7)
	link := filepath.Join(filepath.Dir(source), "symlink.yaml")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	args[2] = link
	err := runIdentity(context.Background(), &bytes.Buffer{}, append(args, "--apply"))
	if err == nil {
		t.Fatal("symlink source accepted")
	}
	if _, err := os.Stat(control); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused source created registry")
	}
}

func TestIdentityMigrationLegacyEnvironmentRequiresExplicitOwner(t *testing.T) {
	ctx := context.Background()
	args, control, _ := identityMigrationFixture(t)
	t.Setenv("TELEGRAM_ALLOWED_USERS", "7, 8")
	t.Setenv("SLACK_ALLOWED_USERS", "T1/U2")
	if err := runIdentity(ctx, &bytes.Buffer{}, append(args, "--legacy-env-user", "unknown", "--apply")); err == nil {
		t.Fatal("legacy environment attributed to an unknown owner")
	}
	if _, err := os.Stat(control); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid owner created registry")
	}
	if err := runIdentity(ctx, &bytes.Buffer{}, append(args, "--legacy-env-user", "alice", "--apply")); err != nil {
		t.Fatal(err)
	}
	path, err := identityhub.StorePath(control, "dev")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := identityhub.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	for _, channel := range []contract.ChannelIdentity{{Channel: contract.Telegram, ID: "7"}, {Channel: contract.Telegram, ID: "8"}, {Channel: contract.Slack, ID: "t1/u2"}} {
		p, err := registry.Resolve(ctx, channel)
		if err != nil || p.ID != "alice" {
			t.Fatalf("legacy channel was not attributed to the specified owner: %v", err)
		}
	}
}
