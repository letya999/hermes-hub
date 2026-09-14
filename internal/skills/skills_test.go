package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestUserSkillsInstallRevokeAndScan(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "hermes", "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	if _, err := Install(home, "notes", "https://example.invalid/notes", "user", []byte("GOOGLE_TOKEN=secret"), true, caller); err == nil {
		t.Fatal("credential skill accepted")
	}
	if _, err := Install(home, "notes", "https://example.invalid/notes", "user", []byte("use org_actions to expand policy"), true, caller); err == nil {
		t.Fatal("mutation skill accepted")
	}
	if _, err := Install(home, "notes", "https://example.invalid/notes", "global", []byte("# Notes\n"), true, caller); err == nil {
		t.Fatal("user wrote global skills")
	}
	rec, err := Install(home, "notes", "https://example.invalid/notes", "user", []byte("# Notes\nSummarize the workspace."), true, caller)
	if err != nil || rec.Digest == "" || rec.EnabledRevision != 1 {
		t.Fatal(rec, err)
	}
	listed, err := Advertised(home)
	if err != nil || len(listed) != 1 || listed[0].Name != "notes" {
		t.Fatalf("%+v %v", listed, err)
	}
	if err := Revoke(home, "notes", caller); err != nil {
		t.Fatal(err)
	}
	listed, err = Advertised(home)
	if err != nil || len(listed) != 0 {
		t.Fatalf("revoked still advertised: %+v", listed)
	}
	if _, err := os.Stat(filepath.Join(home, "hermes", "skills", "notes")); !os.IsNotExist(err) {
		t.Fatal("revoked skill files remained")
	}
	body, _ := os.ReadFile(Path(home))
	if strings.Contains(string(body), "GOOGLE_TOKEN=secret") {
		t.Fatal("registry stored secret skill text")
	}
	if _, err := CopyLimited(strings.NewReader("skill text")); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(home, "missing", caller); err == nil {
		t.Fatal("missing skill revoked")
	}
	if err := os.WriteFile(Path(home), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("corrupt registry loaded")
	}
	if _, err := Install(home, "Bad Name", "", "user", []byte("# x\n"), true, caller); err == nil {
		t.Fatal("invalid skill name accepted")
	}
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(empty), []byte(`{"schema":1,"user":"alice","skills":[{"name":"ghost","scope":"user","revoked":false}]}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err = Advertised(empty)
	if err != nil || len(listed) != 0 {
		t.Fatalf("missing skill advertised: %+v %v", listed, err)
	}
	updated, err := Install(home, "notes", "https://example.invalid/notes", "user", []byte("# Notes\nUpdated."), true, caller)
	if err == nil {
		t.Fatal("install into corrupt registry succeeded")
	}
	_ = updated
}

func TestSkillRevisionConsentAndCuratedRevoke(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "hermes", "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	first, err := Install(home, "notes", "https://example.invalid/notes", "user", []byte("# Notes\nfirst"), true, caller)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Install(home, "notes", "https://example.invalid/notes", "user", []byte("# Notes\nsecond"), true, caller)
	if err != nil || second.EnabledRevision != first.EnabledRevision+1 || second.RollbackRevision != first.EnabledRevision {
		t.Fatal(second, err)
	}
	if _, err := Install(home, "notes", "", "user", []byte("# Notes\nx"), false, caller); err == nil {
		t.Fatal("consent skipped")
	}
	if _, err := Install(home, "notes", "", "user", []byte("# Notes\nx"), true, identity.Envelope{}); err == nil {
		t.Fatal("empty owner accepted")
	}
	if err := os.WriteFile(Path(home), []byte(`{"schema":0,"user":"alice","skills":[]}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(home)
	if err != nil || reg.Schema != Schema {
		t.Fatal(reg, err)
	}
	if err := os.WriteFile(Path(home), []byte(`{"schema":2,"user":"alice"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("unsupported schema loaded")
	}
	if err := os.WriteFile(Path(home), []byte(`{"schema":1,"user":"alice","skills":[{"name":"curated","scope":"org"}]}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(home, "curated", caller); err == nil {
		t.Fatal("curated skill revoked from user home")
	}
	if err := Revoke(home, "notes", identity.Envelope{}); err == nil {
		t.Fatal("invalid caller revoked")
	}
}
