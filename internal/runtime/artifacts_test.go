package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeArtifact(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScanArtifactsBoundsWindowAndSafety(t *testing.T) {
	dir := t.TempDir()
	old := workspace
	workspace = dir
	defer func() { workspace = old }()
	root := artifactRoot()
	writeArtifact(t, root, "documents/report.pdf", "pdf-bytes")
	writeArtifact(t, root, "images/pic.png", "png-bytes")
	oldFile := writeArtifact(t, root, "documents/old.txt", "stale")
	if err := os.Chtimes(oldFile, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	writeArtifact(t, root, "drafts/scratch.md", "not a deliverable")
	writeArtifact(t, root, "documents/empty.txt", "")
	big := writeArtifact(t, root, "documents/big.bin", "")
	if err := os.Truncate(big, artifactMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	outside := writeArtifact(t, filepath.Join(dir, "other"), "x.txt", "outside")
	link := filepath.Join(root, "documents", "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		link = "" // no symlink privilege on this platform
	}
	got := scanArtifacts(time.Now().Add(-time.Minute))
	var paths []string
	for _, ref := range got {
		paths = append(paths, ref.Path)
	}
	if len(paths) != 2 || paths[0] != "documents/report.pdf" || paths[1] != "images/pic.png" {
		t.Fatalf("artifacts=%v", paths)
	}
	if got[0].Mime != "application/pdf" || got[1].Mime != "image/png" {
		t.Fatalf("mimes=%+v", got)
	}
	if scanArtifacts(time.Time{}) != nil {
		t.Fatal("unattributed observation listed artifacts")
	}
	if link != "" {
		if _, err := os.Lstat(link); err == nil {
			t.Log("symlink present and excluded")
		}
	}
}

func TestArtifactEndpointValidationAndServing(t *testing.T) {
	dir := t.TempDir()
	old := workspace
	workspace = dir
	defer func() { workspace = old }()
	writeArtifact(t, artifactRoot(), "documents/report.pdf", "pdf-bytes")
	writeArtifact(t, filepath.Join(dir, "secret"), "hidden.txt", "nope")
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	handler := runtimeHandler()
	call := func(name, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/artifact", strings.NewReader(`{"name":`+quote(name)+`}`))
		req.Header.Set("Authorization", "Bearer "+auth)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("documents/report.pdf", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d", rec.Code)
	}
	for _, name := range []string{"../x", "documents/../hidden.txt", "documents", "images/", "other/x.txt", "documents//x", "/documents/x"} {
		if rec := call(name, "secret"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q → %d, want 400", name, rec.Code)
		}
	}
	if rec := call("documents/missing.pdf", "secret"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing=%d", rec.Code)
	}
	rec := call("documents/report.pdf", "secret")
	if rec.Code != http.StatusOK || rec.Body.String() != "pdf-bytes" || !strings.Contains(rec.Header().Get("Content-Type"), "pdf") {
		t.Fatalf("serve status=%d type=%q body=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	link := filepath.Join(artifactRoot(), "documents", "link.txt")
	if err := os.Symlink(filepath.Join(dir, "secret", "hidden.txt"), link); err == nil {
		if rec := call("documents/link.txt", "secret"); rec.Code == http.StatusOK {
			t.Fatal("symlinked artifact served")
		}
	}
}

func TestExtractMediaArtifactsStagesWorkspaceFiles(t *testing.T) {
	dir := t.TempDir()
	old := workspace
	workspace = dir
	defer func() { workspace = old }()
	png := writeArtifact(t, dir, "dandelion.png", "png-bytes")
	writeArtifact(t, artifactRoot(), "documents/plain.md", "already")
	text := "MEDIA:" + png + "\n\nОдуванчик готов.\n\nMEDIA: artifacts/documents/plain.md\nMEDIA: " + filepath.Join(dir, "..", "escape.txt") + "\nMEDIA: missing.bin\n"
	clean, refs := extractMediaArtifacts(text)
	if strings.Contains(clean, "MEDIA:") || clean != "Одуванчик готов." {
		t.Fatalf("markers leaked into text: %q", clean)
	}
	if len(refs) != 4 {
		t.Fatalf("refs=%+v", refs)
	}
	if refs[0].Path != "images/dandelion.png" || refs[0].Mime != "image/png" || refs[0].Size != 9 {
		t.Fatalf("staged ref=%+v", refs[0])
	}
	staged, err := os.ReadFile(filepath.Join(artifactRoot(), "images", "dandelion.png"))
	if err != nil || string(staged) != "png-bytes" {
		t.Fatalf("staged bytes: %v %q", err, staged)
	}
	if refs[1].Path != "documents/plain.md" {
		t.Fatalf("in-place ref=%+v", refs[1])
	}
	if refs[2].Error == "" || refs[3].Error == "" || refs[3].Name != "missing.bin" {
		t.Fatalf("unusable media must surface as error refs: %+v", refs[2:])
	}
}

func TestExtractMediaArtifactsDropsEmptyAndDupes(t *testing.T) {
	dir := t.TempDir()
	old := workspace
	workspace = dir
	defer func() { workspace = old }()
	writeArtifact(t, dir, "a.txt", "a")
	text, refs := extractMediaArtifacts("MEDIA:a.txt\nMEDIA: a.txt\nMEDIA:\n")
	if len(refs) != 1 || refs[0].Path != "documents/a.txt" {
		t.Fatalf("refs=%+v", refs)
	}
	if text != "" {
		t.Fatalf("marker-only text must clean to empty, got %q", text)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
