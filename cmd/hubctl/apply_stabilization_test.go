package main

import (
	"context"
	"github.com/letya999/hermes-hub/internal/communication"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestM10ApplyPrevalidatesEveryResourceAndBoundsInput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.md")
	if err := os.WriteFile(source, []byte("snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	good := `routines:
  - id: reminder
    organization_id: personal
    runtime_id: alice
    policy_version: policy-1
    channel: telegram_bot
    chat_id: 11
    kind: reminder
    timezone: UTC
    expression: "* * * * *"
    input: hello
`
	for _, test := range []struct{ name, body string }{
		{"unknown", good + "unknown: true\n"},
		{"multiple", good + "---\nknowledge: []\n"},
		{"bad knowledge", good + "knowledge:\n  - name: ../outside\n    source: source.md\n"},
		{"missing source", good + "knowledge:\n  - name: notes.md\n    source: missing\n"},
		{"duplicate knowledge", good + "knowledge:\n  - name: notes.md\n    source: source.md\n  - name: notes.md\n    source: source.md\n"},
		{"duplicate routine", good + strings.TrimPrefix(good, "routines:\n")},
		{"implicit route", strings.Replace(good, "    policy_version: policy-1\n", "", 1)},
		{"offline check", strings.Replace(good, "kind: reminder", "kind: check", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := filepath.Join(root, test.name+".yaml")
			if err := os.WriteFile(spec, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			spool := filepath.Join(t.TempDir(), "spool")
			if _, err := applyFile(spool, "alice", spec); err == nil {
				t.Fatal("invalid apply accepted")
			}
			files, _ := filepath.Glob(filepath.Join(spool, "schedules", "*.json"))
			if len(files) != 0 {
				t.Fatal("late validation failure left routines")
			}
		})
	}
	spec := filepath.Join(root, "valid.yaml")
	if err := os.WriteFile(spec, []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(root, "locked")
	unlock, err := communication.LockSpool(spool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = applyFile(spool, "alice", spec); err == nil {
		unlock()
		t.Fatal("offline apply ignored gateway lock")
	}
	unlock()
	if _, err = applyFile(spool, "../alice", spec); err == nil {
		t.Fatal("invalid principal accepted")
	}
	oversized := filepath.Join(root, "too-big.yaml")
	if err = os.WriteFile(oversized, []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = applyFile(spool, "alice", oversized); err == nil {
		t.Fatal("oversize YAML accepted")
	}
	if _, err = readApplySource(root); err == nil {
		t.Fatal("directory accepted as YAML")
	}
	if _, err = readKnowledgeSource(root); err == nil {
		t.Fatal("directory accepted as knowledge")
	}
	if _, err = applyKnowledge(spool, "alice", "notes.md", source); err != nil {
		t.Fatal(err)
	}
	// No initial-file read may follow a sibling user's link.
	victim := filepath.Join(spool, "knowledge", "bob")
	if err = os.MkdirAll(victim, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(victim, "private.md"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(spool, "knowledge", "linked")
	if err = os.Symlink(victim, owner); err != nil {
		t.Log("symlink test unavailable on this host")
		return
	}
	if _, err = applyKnowledge(spool, "linked", "private.md", source); err == nil {
		t.Fatal("knowledge owner symlink accepted")
	}
	bytes, err := os.ReadFile(filepath.Join(victim, "private.md"))
	if err != nil || string(bytes) != "private" {
		t.Fatal("sibling knowledge overwritten")
	}
}

func TestM10OfflineRoutineRequiresRoutingAndLock(t *testing.T) {
	root := t.TempDir()
	args := []string{"create", "--spool", root, "--user", "alice", "--id", "one", "--expr", "* * * * *", "--input", "work"}
	if err := runRoutine(context.Background(), args); err == nil {
		t.Fatal("implicit recipient accepted")
	}
	routed := append(args, "--chat-id", "11", "--organization", "personal", "--runtime", "alice", "--policy", "policy-1")
	if err := runRoutine(context.Background(), routed); err != nil {
		t.Fatal(err)
	}
	unlock, err := communication.LockSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = runRoutine(context.Background(), []string{"delete", "--spool", root, "--user", "alice", "--id", "one", "--chat-id", "11", "--organization", "personal", "--runtime", "alice", "--policy", "policy-1"}); err == nil {
		unlock()
		t.Fatal("routine ignored writer lock")
	}
	unlock()
}
