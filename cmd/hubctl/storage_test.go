package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// `hubctl storage status` prints only allowlisted aggregates per owner:
// bytes, files, limits and a safe status word. Absolute paths, file names
// and unknown entries never reach the output.
func TestStorageStatusAllowlistedFieldsOnly(t *testing.T) {
	spoolDir := t.TempDir()
	for _, dir := range []string{"pending", "done", "failed"} {
		if err := os.MkdirAll(filepath.Join(spoolDir, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := toolhub.NewStore().Save(storePath); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runStorageStatus(&out, spoolDir, storePath); err != nil {
		t.Fatalf("storage status: %v", err)
	}
	var report struct {
		Owners []struct {
			Owner        string `json:"owner"`
			Status       string `json:"status"`
			LogicalBytes int64  `json:"logical_bytes"`
			Files        int    `json:"files"`
			LimitBytes   int64  `json:"limit_bytes"`
			LimitFiles   int    `json:"limit_files"`
			Complete     bool   `json:"complete"`
		} `json:"owners"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("status output is not the allowlisted JSON contract: %v", err)
	}
	if len(report.Owners) != 2 {
		t.Fatalf("expected both owners reported: %s", out.String())
	}
	for _, owner := range report.Owners {
		if owner.Status != "ok" || !owner.Complete {
			t.Fatalf("owner %s not cleanly accounted: %+v", owner.Owner, owner)
		}
	}
	raw := out.String()
	for _, leaked := range []string{spoolDir, storePath, "store.json", ".refresh-leases", "pending/"} {
		if strings.Contains(raw, leaked) {
			t.Fatalf("status output leaks an object name or path (%q): %s", leaked, raw)
		}
	}
}

// The command must be reachable through the hubctl dispatch, not only as a
// package-internal helper.
func TestStorageStatusCommandWiring(t *testing.T) {
	spoolDir := t.TempDir()
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := toolhub.NewStore().Save(storePath); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"storage", "status", "--spool", spoolDir, "--toolhub-store", storePath}); err != nil {
		t.Fatalf("storage status dispatch failed: %v", err)
	}
}

func TestStorageStatusWithOmittedSpool(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := toolhub.NewStore().Save(storePath); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runStorageStatus(&out, "", storePath); err != nil {
		t.Fatal(err)
	}
	var report storageStatusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Owners) != 2 || report.Owners[0].Status != "unavailable" || report.Owners[1].Status != "ok" {
		t.Fatalf("unexpected owner availability: %+v", report.Owners)
	}
	if err := runStorageCleanup(io.Discard, "", true, "sha256:ignored", time.Hour); err == nil {
		t.Fatal("cleanup accepted an empty spool path")
	}
}

// A cleanup plan binds its scope and the exact candidate set by digest:
// applying a digest computed for different content — or for a foreign
// scope — is rejected instead of deleting whatever happens to be there.
func TestCleanupStalePlanAndForeignScopeRejected(t *testing.T) {
	spoolDir := t.TempDir()
	doneDir := filepath.Join(spoolDir, "done")
	if err := os.MkdirAll(doneDir, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(doneDir, "job-1.json")
	if err := os.WriteFile(record, []byte(`{"job_id":"job-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(record, old, old); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runStorageCleanup(&out, spoolDir, false, "", 72*time.Hour); err != nil {
		t.Fatalf("dry-run cleanup: %v", err)
	}
	var plan struct {
		Scope  string `json:"scope"`
		Digest string `json:"digest"`
		Files  int    `json:"files"`
		Bytes  int64  `json:"bytes"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.Digest == "" || plan.Files == 0 {
		t.Fatalf("dry-run did not emit a bound plan: %s", out.String())
	}
	if strings.Contains(out.String(), "job-1.json") || strings.Contains(out.String(), spoolDir) {
		t.Fatalf("plan leaks object names or paths: %s", out.String())
	}
	if err := runStorageCleanup(io.Discard, spoolDir, true, "sha256:foreign", 72*time.Hour); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("foreign plan digest accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(doneDir, "job-2.json"), []byte(`{"job_id":"job-2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runStorageCleanup(io.Discard, spoolDir, true, plan.Digest, 72*time.Hour); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale plan accepted after content drift: %v", err)
	}
}

// Cleanup may only touch owner-regular files: a symlink planted inside the
// owned tree is refused, not followed, on both plan and apply paths.
func TestCleanupSymlinkEscapeDenied(t *testing.T) {
	spoolDir := t.TempDir()
	doneDir := filepath.Join(spoolDir, "done")
	if err := os.MkdirAll(doneDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(doneDir, "escape.json")); err != nil {
		t.Fatal(err)
	}
	if err := runStorageCleanup(io.Discard, spoolDir, false, "", 72*time.Hour); err == nil {
		t.Fatal("plan followed a symlinked record")
	}
}

// A verified apply runs the bounded compaction the digest approved: aged
// terminal records leave the hot dir into the archive bundle, and the live
// pending records stay.
func TestCleanupApplyArchivesPlannedRecords(t *testing.T) {
	spoolDir := t.TempDir()
	spool, err := communication.OpenSpool(spoolDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = spool
	doneDir := filepath.Join(spoolDir, "done")
	record := filepath.Join(doneDir, "job-1.json")
	if err := os.WriteFile(record, []byte(`{"job_id":"job-1","status":"done"}`), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(record, old, old); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runStorageCleanup(&out, spoolDir, false, "", 72*time.Hour); err != nil {
		t.Fatalf("dry-run cleanup: %v", err)
	}
	var plan struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.Digest == "" {
		t.Fatalf("dry-run did not emit a bound plan: %s", out.String())
	}
	if err := runStorageCleanup(io.Discard, spoolDir, true, plan.Digest, 72*time.Hour); err != nil {
		t.Fatalf("verified apply rejected: %v", err)
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		t.Fatal("aged terminal record was not archived out of the hot dir")
	}
	entries, err := os.ReadDir(filepath.Join(spoolDir, "archive"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("terminal record did not reach the archive bundle: %v", err)
	}
}

func TestCleanupApplyEnforcesGatewayFileLimit(t *testing.T) {
	t.Setenv("HUB_SPOOL_MAX_FILES", "2")
	dir := t.TempDir()
	if _, err := communication.OpenSpool(dir); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		path := filepath.Join(dir, "done", fmt.Sprintf("job-%d.json", i))
		if err := os.WriteFile(path, []byte(`{"job_id":"done"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := runStorageCleanup(&out, dir, false, "", 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if err := runStorageCleanup(io.Discard, dir, true, plan.Digest, 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(dir, "done"))
	if err != nil || len(files) != 2 {
		t.Fatalf("terminal files after bounded cleanup = %d, error = %v", len(files), err)
	}
	archive, err := os.ReadDir(filepath.Join(dir, "archive"))
	if err != nil || len(archive) == 0 {
		t.Fatalf("overflow was not archived: %v", err)
	}
}

// The bare `storage` dispatch reports a complete-word usage error: the
// project lint contract (ST1005) rejects messages ending in punctuation.
func TestStorageUsageErrorIsCompleteWords(t *testing.T) {
	err := run(context.Background(), []string{"storage"})
	if err == nil || !strings.HasPrefix(err.Error(), "usage: hubctl storage") {
		t.Fatalf("unexpected usage error: %v", err)
	}
	if strings.HasSuffix(err.Error(), ".") {
		t.Fatalf("usage error ends with punctuation: %q", err.Error())
	}
}

// Cleanup is reachable through hubctl dispatch as an explicit dry-run;
// an apply without the plan digest is refused before touching the owner.
func TestCleanupCommandWiring(t *testing.T) {
	spoolDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spoolDir, "done"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"storage", "cleanup", "--spool", spoolDir, "--retention", "72h"}); err != nil {
		t.Fatalf("cleanup dry-run dispatch failed: %v", err)
	}
	if err := run(context.Background(), []string{"storage", "cleanup", "--spool", spoolDir, "--apply"}); err == nil {
		t.Fatal("apply dispatch accepted without a plan digest")
	}
	if err := run(context.Background(), []string{"storage", "cleanup", "--spool", spoolDir, "--apply", "--digest", "sha256:bogus"}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("apply dispatch accepted a foreign digest: %v", err)
	}
}
