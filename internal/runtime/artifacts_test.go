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
	// Videos ride the same endpoint under the wider bound.
	writeArtifact(t, artifactRoot(), "videos/clip.mp4", "mp4-bytes")
	rec = call("videos/clip.mp4", "secret")
	if rec.Code != http.StatusOK || rec.Body.String() != "mp4-bytes" || rec.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("video serve status=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
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

func TestExtractVoiceLines(t *testing.T) {
	// VOICE: lines are stripped from the visible reply and joined into the
	// spoken payload; a marker-only reply cleans to empty text.
	text, voice := extractVoiceLines("Смотри на таблицу ниже.\n\nVOICE: а голосом: всё готово\nVOICE: и ещё фраза\n")
	if text != "Смотри на таблицу ниже." || voice != "а голосом: всё готово\nи ещё фраза" {
		t.Fatalf("extractVoice: %q %q", text, voice)
	}
	text, voice = extractVoiceLines("VOICE: только голосом")
	if text != "" || voice != "только голосом" {
		t.Fatalf("voice-only: %q %q", text, voice)
	}
	// Empty payloads and VOICE-less text are untouched.
	if text, voice = extractVoiceLines("VOICE:\nпросто текст"); text != "просто текст" || voice != "" {
		t.Fatalf("empty marker: %q %q", text, voice)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestScanAndStageVideoArtifacts(t *testing.T) {
	dir := t.TempDir()
	old := workspace
	workspace = dir
	defer func() { workspace = old }()
	root := artifactRoot()
	writeArtifact(t, root, "videos/clip.mp4", "mp4-bytes")
	// A video under the 48 MiB bound passes where the 8 MiB doc cap would fail.
	big := writeArtifact(t, root, "videos/big.mp4", "")
	if err := os.Truncate(big, 20<<20); err != nil {
		t.Fatal(err)
	}
	// A document the same size is still refused by the tighter bound.
	bigDoc := writeArtifact(t, root, "documents/big.bin", "")
	if err := os.Truncate(bigDoc, 20<<20); err != nil {
		t.Fatal(err)
	}
	got := scanArtifacts(time.Now().Add(-time.Minute))
	var clip, bigRef *ArtifactRef
	for i, ref := range got {
		if ref.Path == "videos/clip.mp4" {
			clip = &got[i]
		}
		if ref.Path == "videos/big.mp4" {
			bigRef = &got[i]
		}
	}
	if clip == nil || clip.Mime != "video/mp4" || bigRef == nil {
		t.Fatalf("video artifacts: %+v", got)
	}
	for _, ref := range got {
		if ref.Path == "documents/big.bin" {
			t.Fatal("oversized document scanned")
		}
	}
	// MEDIA: marker on an mp4 stages into the videos bucket.
	vid := writeArtifact(t, dir, "movie.mp4", "movie-bytes")
	_, refs := extractMediaArtifacts("MEDIA:" + vid)
	if len(refs) != 1 || refs[0].Path != "videos/movie.mp4" || refs[0].Mime != "video/mp4" {
		t.Fatalf("video staging: %+v", refs)
	}
}

func TestMergeArtifactsDedupesAndBounds(t *testing.T) {
	a := []ArtifactRef{{Path: "images/a.png", Size: 1}, {Path: "images/b.png", Size: 2}}
	b := []ArtifactRef{{Path: "images/a.png", Size: 9}, {Name: "gone.png", Error: "x"}, {Path: "videos/c.mp4", Mime: "video/mp4"}}
	got := mergeArtifacts(a, b)
	if len(got) != 4 || got[0].Size != 1 || got[1].Path != "images/b.png" || got[2].Error != "x" || got[3].Mime != "video/mp4" {
		t.Fatalf("merge: %+v", got)
	}
	// Error refs dedupe on name+error, not on path.
	dupes := mergeArtifacts([]ArtifactRef{{Name: "x", Error: "e"}}, []ArtifactRef{{Name: "x", Error: "e"}})
	if len(dupes) != 1 {
		t.Fatalf("error dedupe: %+v", dupes)
	}
	// The artifact cap applies across merged groups.
	var many []ArtifactRef
	for i := 0; i < artifactMaxCount+3; i++ {
		many = append(many, ArtifactRef{Path: strings.Repeat("x", i+1)})
	}
	if out := mergeArtifacts(many); len(out) != artifactMaxCount {
		t.Fatalf("cap: %d", len(out))
	}
	// copyBounded: missing source errors; a normal copy lands the bytes.
	dir := t.TempDir()
	if err := copyBounded(filepath.Join(dir, "none.bin"), filepath.Join(dir, "out.bin"), 4); err == nil {
		t.Fatal("missing source copied")
	}
	src := writeArtifact(t, dir, "in.bin", "0123456789")
	dst := filepath.Join(dir, "out.bin")
	if err := copyBounded(src, dst, 4); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "0123" {
		t.Fatalf("bounded copy: %q", got)
	}
}

func TestArtifactLimitBuckets(t *testing.T) {
	if artifactLimit("videos/clip.mp4") != videoMaxBytes {
		t.Fatal("video bucket bound")
	}
	if artifactLimit("documents/x.pdf") != artifactMaxBytes || artifactLimit("images/x.png") != artifactMaxBytes {
		t.Fatal("default bound")
	}
}
