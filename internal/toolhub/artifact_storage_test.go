package toolhub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestArtifactStorageWriteOnceConcurrentReuse(t *testing.T) {
	directory := t.TempDir()
	data, _ := ociFixture(t, "artifact")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	var wait sync.WaitGroup
	results := make(chan StoredOCIArtifact, 16)
	errorsFound := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			stored, err := PersistOCIArtifact(context.Background(), directory, bytes.NewReader(data), 1<<20)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- stored
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	for result := range results {
		if result.ArchiveDigest != digest || result.Size != int64(len(data)) || result.Evidence.ProvenanceDigest == "" {
			t.Fatalf("bad result %+v", result)
		}
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 || files[0].Name() != artifactObjectName(digest) {
		t.Fatalf("unexpected files %v err=%v", files, err)
	}
	file, stored, err := OpenStoredOCIArtifact(context.Background(), directory, digest, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, data) || stored.ArchiveDigest != digest {
		t.Fatal("reopened object changed")
	}
}

func TestArtifactStorageRejectsCorruptionWithoutOverwrite(t *testing.T) {
	directory := t.TempDir()
	data, _ := ociFixture(t, "legacy")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	path := filepath.Join(directory, artifactObjectName(digest))
	corrupted := bytes.Repeat([]byte("x"), len(data))
	if err := os.WriteFile(path, corrupted, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistOCIArtifact(context.Background(), directory, bytes.NewReader(data), 1<<20); !errors.Is(err, ErrConflict) {
		t.Fatalf("corruption accepted: %v", err)
	}
	if file, _, err := OpenStoredOCIArtifact(context.Background(), directory, digest, 1<<20); !errors.Is(err, ErrConflict) {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("corrupt object opened: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, corrupted) {
		t.Fatal("existing object overwritten")
	}
	files, _ := os.ReadDir(directory)
	if len(files) != 1 {
		t.Fatal("temporary object leaked")
	}
}

func TestArtifactStorageLimitsCancellationAndInvalidInputs(t *testing.T) {
	directory := t.TempDir()
	data, _ := ociFixture(t, "legacy")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		ctx    context.Context
		source io.Reader
		limit  int64
	}{
		{context.Background(), nil, 1024}, {context.Background(), bytes.NewReader(data), 0},
		{context.Background(), bytes.NewReader(data), 9 << 30}, {context.Background(), bytes.NewReader(data), 1},
		{context.Background(), bytes.NewReader([]byte("invalid OCI")), 1024}, {cancelled, bytes.NewReader(data), 1 << 20},
	} {
		if _, err := PersistOCIArtifact(test.ctx, directory, test.source, test.limit); err == nil {
			t.Fatal("invalid input persisted")
		}
	}
	if _, err := PersistOCIArtifact(context.Background(), "relative", bytes.NewReader(data), 1<<20); err == nil {
		t.Fatal("relative directory accepted")
	}
	if _, err := PersistOCIArtifact(context.Background(), filepath.Join(directory, "missing"), bytes.NewReader(data), 1<<20); err == nil {
		t.Fatal("missing directory accepted")
	}
	files, _ := os.ReadDir(directory)
	if len(files) != 0 {
		t.Fatal("rejected input published or temporary file leaked")
	}
	for _, digest := range []string{"../foreign", "sha256:invalid"} {
		if file, _, err := OpenStoredOCIArtifact(context.Background(), directory, digest, 1024); err == nil {
			_ = file.Close()
			t.Fatal("unsafe digest opened")
		}
	}
	stored, err := PersistOCIArtifact(context.Background(), directory, bytes.NewReader(data), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if file, _, err := OpenStoredOCIArtifact(cancelled, directory, stored.ArchiveDigest, 1<<20); !errors.Is(err, context.Canceled) {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("cancelled read accepted %v", err)
	}
	if file, _, err := OpenStoredOCIArtifact(context.Background(), directory, stored.ArchiveDigest, 1); err == nil {
		_ = file.Close()
		t.Fatal("oversized object opened")
	}
}

func TestArtifactStorageRejectsForeignSymlink(t *testing.T) {
	directory, foreign := t.TempDir(), t.TempDir()
	data, _ := ociFixture(t, "legacy")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	target := filepath.Join(foreign, "other-user.oci.tar")
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, artifactObjectName(digest))); err != nil {
		t.Skipf("symlink capability unavailable: %v", err)
	}
	if file, _, err := OpenStoredOCIArtifact(context.Background(), directory, digest, 1<<20); err == nil {
		_ = file.Close()
		t.Fatal("foreign symlink opened")
	}
	if _, err := PersistOCIArtifact(context.Background(), directory, bytes.NewReader(data), 1<<20); err == nil {
		t.Fatal("foreign symlink reused")
	}
}
