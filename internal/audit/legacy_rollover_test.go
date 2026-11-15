package audit

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func legacyFixture(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	var body []byte
	for _, principal := range []string{"alice", "bob"} {
		e := NewEvent("tool-call", principal, "allowed")
		e.Schema = 1
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, append(line, '\n')...)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path, body
}

func TestLegacyRolloverPreservesArchiveAndRestartsBoundedLedger(t *testing.T) {
	path, original := legacyFixture(t)
	plan, err := PlanLegacyRollover(path)
	if err != nil || plan.Records != 2 || plan.Bytes != int64(len(original)) {
		t.Fatalf("invalid plan: %+v %v", plan, err)
	}
	if _, err := RolloverLegacy(path, "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, ErrStaleLegacyPlan) {
		t.Fatalf("stale plan accepted: %v", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != string(original) {
		t.Fatalf("stale plan changed source: %v", err)
	}
	if applied, err := RolloverLegacy(path, plan.Digest); err != nil || applied != plan {
		t.Fatalf("rollover failed: %+v %v", applied, err)
	}
	archive := path + ".legacy-" + strings.TrimPrefix(plan.Digest, "sha256:") + ".jsonl.gz"
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := io.ReadAll(gz)
	if err != nil || string(archived) != string(original) {
		t.Fatalf("archive differs from source: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archive + ".raw"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncompressed duplicate remains: %v", err)
	}
	ledger, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := ledger.ReadPage(Scope{PrincipalID: "operator"}, 0, 10)
	if err != nil || len(page.Events) != 1 || page.Events[0].Name != plan.Digest {
		t.Fatalf("rollover marker unavailable: %v", err)
	}
	if err := ledger.Append(NewEvent("tool-call", "alice", "allowed")); err != nil {
		t.Fatalf("new ledger cannot append: %v", err)
	}
}

func TestLegacyRolloverRejectsMixedAndUnsafeSources(t *testing.T) {
	path, _ := legacyFixture(t)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(NewEvent("job", "alice", "allowed"))
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := PlanLegacyRollover(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("schema-2 mixed audit accepted: %v", err)
	}
	if err := os.WriteFile(path, []byte("{\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanLegacyRollover(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt audit accepted: %v", err)
	}
	outside, _ := legacyFixture(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := PlanLegacyRollover(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink audit accepted: %v", err)
	}
}
