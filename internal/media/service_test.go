package media

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serviceStub emulates the hub-media sidecar: bearer check, image endpoints,
// the async job API and bounded result downloads.
func serviceStub(t *testing.T, status string, result []byte, resultMime string) (*httptest.Server, *strings.Builder) {
	t.Helper()
	seen := &strings.Builder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.WriteString(r.Method + " " + r.URL.Path + "\n")
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/images/generations":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["model"] == "" || req["prompt"] == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(result)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/images/edits":
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f, _, err := r.FormFile("image")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.Close()
			w.Header().Set("Content-Type", "image/png")
			w.Write(result)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["kind"] != "video" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"id": "job-1", "status": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job-1":
			json.NewEncoder(w).Encode(map[string]string{"id": "job-1", "status": status, "kind": "video"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job-1/result":
			w.Header().Set("Content-Type", resultMime)
			w.Write(result)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestServiceModeImageGeneration(t *testing.T) {
	pngBody := pngBytes(t, 2, 2)
	srv, _ := serviceStub(t, "done", pngBody, "image/png")
	s, dir := openSession(t)
	grantFile(t, dir, srv.URL, true)
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	t.Setenv("FAL_KEY", "") // runtime holds no provider credential in service mode
	t.Setenv("OPENAI_API_KEY", "")

	out, err := s.Generate(context.Background(), "pic", "a red square")
	if err != nil {
		t.Fatal(err)
	}
	if out["provider"] != "hub-media" || out["path"] != "artifacts/images/pic.png" {
		t.Fatalf("service generate: %v", out)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "artifacts", "images", "pic.png"))
	if err != nil || !bytes.Equal(stored, pngBody) {
		t.Fatal(err)
	}
}

func TestServiceModeImageEdit(t *testing.T) {
	pngBody := pngBytes(t, 2, 2)
	srv, _ := serviceStub(t, "done", pngBody, "image/png")
	s, dir := openSession(t)
	grantFile(t, dir, srv.URL, true)
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	writeWorkspace(t, dir, "src.png", pngBody)

	out, err := s.Edit(context.Background(), "edited", "src.png", "make it blue")
	if err != nil {
		t.Fatal(err)
	}
	if out["path"] != "artifacts/images/edited.png" {
		t.Fatalf("service edit: %v", out)
	}
}

func TestServiceModeAuthPropagates(t *testing.T) {
	pngBody := pngBytes(t, 1, 1)
	srv, _ := serviceStub(t, "done", pngBody, "image/png")
	s, dir := openSession(t)
	grantFile(t, dir, srv.URL, true)
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "wrong-token")
	if _, err := s.Generate(context.Background(), "pic", "cat"); err == nil {
		t.Fatal("generate succeeded against a rejecting service")
	}
}

func TestServiceModeVideoJob(t *testing.T) {
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	srv, seen := serviceStub(t, "done", mp4, "video/mp4")
	s, dir := openSession(t)
	grantFile(t, dir, srv.URL, true)
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")

	out, err := s.VideoGenerate(context.Background(), "a rocket launch")
	if err != nil || out["job_id"] != "job-1" {
		t.Fatalf("video_generate: %v %v", out, err)
	}
	got, err := s.MediaFetch(context.Background(), "job-1", "clip")
	if err != nil {
		t.Fatal(err)
	}
	if got["path"] != "artifacts/videos/clip.mp4" || got["mime"] != "video/mp4" {
		t.Fatalf("media_fetch: %v", got)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "artifacts", "videos", "clip.mp4"))
	if err != nil || !bytes.Equal(stored, mp4) {
		t.Fatal(err)
	}
	if !strings.Contains(seen.String(), "GET /v1/jobs/job-1/result") {
		t.Fatalf("result never fetched: %s", seen.String())
	}
}

func TestMediaFetchPendingAndMissing(t *testing.T) {
	srv, _ := serviceStub(t, "running", nil, "")
	s, dir := openSession(t)
	grantFile(t, dir, srv.URL, true)
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")

	out, err := s.MediaFetch(context.Background(), "job-1", "clip")
	if err != nil || out["status"] != "running" {
		t.Fatalf("pending fetch: %v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts", "videos")); !os.IsNotExist(err) {
		t.Fatal("pending job staged a file")
	}
	if _, err := s.MediaFetch(context.Background(), "job-../escape", "clip"); err == nil {
		t.Fatal("traversal job id accepted")
	}
}

func TestServiceModeRequiredForAsync(t *testing.T) {
	s, dir := openSession(t)
	grantFile(t, dir, "http://127.0.0.1:9", true)
	t.Setenv("HUB_MEDIA_URL", "")
	if _, err := s.VideoGenerate(context.Background(), "x"); err == nil {
		t.Fatal("video submit without service")
	}
	if _, err := s.MediaFetch(context.Background(), "job-1", "clip"); err == nil {
		t.Fatal("fetch without service")
	}
}

func TestMediaExtAndJobIDTables(t *testing.T) {
	for mime, want := range map[string]string{
		"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp",
		"video/mp4": "mp4", "video/webm": "webm", "video/quicktime": "mov",
	} {
		if got := mediaExt(mime); got != want {
			t.Fatalf("%s: %q", mime, got)
		}
	}
	for _, mime := range []string{"text/html", "application/pdf", "image/gif", ""} {
		if mediaExt(mime) != "" {
			t.Fatalf("unsafe mime mapped: %s", mime)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", strings.Repeat("x", 65), "job;rm"} {
		if validJobID(bad) {
			t.Fatalf("bad job id accepted: %q", bad)
		}
	}
}

func TestMediaFetchErrorBranches(t *testing.T) {
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	srv, _ := serviceStub(t, "failed", mp4, "video/mp4")
	s, dir := openSession(t)
	grantFile(t, dir, srv.URL, true)
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")

	if _, err := s.MediaFetch(context.Background(), "job-1", "clip"); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("failed job not reported: %v", err)
	}
	if _, err := s.MediaFetch(context.Background(), "job-1", "../escape"); err == nil {
		t.Fatal("traversal name accepted")
	}
	// A non-200 status surfaces an error.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer dead.Close()
	t.Setenv("HUB_MEDIA_URL", dead.URL)
	if _, err := s.MediaFetch(context.Background(), "job-1", "clip"); err == nil {
		t.Fatal("bad gateway accepted")
	}
	if _, err := s.VideoGenerate(context.Background(), "x"); err == nil {
		t.Fatal("submit against bad gateway accepted")
	}
}

func TestServiceContractErrors(t *testing.T) {
	s, dir := openSession(t)
	grantFile(t, dir, "http://127.0.0.1:9", true)

	// Wrong mime in the image response is refused before any file lands.
	mislabeled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>"))
	}))
	defer mislabeled.Close()
	t.Setenv("HUB_MEDIA_URL", mislabeled.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	if _, err := s.Generate(context.Background(), "pic", "x"); err == nil {
		t.Fatal("non-image payload accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts")); !os.IsNotExist(err) {
		t.Fatal("bad payload staged a file")
	}

	// Job lookups surface 404 and empty ids explicitly.
	jobs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/jobs/missing":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/v1/jobs" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"status":"queued"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer jobs.Close()
	t.Setenv("HUB_MEDIA_URL", jobs.URL)
	if _, err := s.MediaFetch(context.Background(), "missing", "clip"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing job: %v", err)
	}
	if _, err := s.VideoGenerate(context.Background(), "x"); err == nil {
		t.Fatal("job submit without id accepted")
	}
}

func TestServiceDoBoundsAndMime(t *testing.T) {
	s, dir := openSession(t)
	grantFile(t, dir, "http://127.0.0.1:9", true)
	// Empty body is outside bounds.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
	}))
	defer empty.Close()
	t.Setenv("HUB_MEDIA_URL", empty.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	if _, err := s.Generate(context.Background(), "pic", "x"); err == nil {
		t.Fatal("empty body accepted")
	}
	// Header claims image but bytes sniff as junk → refused by sniffImage.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("not actually png"))
	}))
	defer fake.Close()
	t.Setenv("HUB_MEDIA_URL", fake.URL)
	if _, err := s.Generate(context.Background(), "pic", "x"); err == nil {
		t.Fatal("mislabeled bytes accepted")
	}
	// Done job whose result mime is unsupported is refused by mediaExt.
	badResult := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/jobs/job-9":
			json.NewEncoder(w).Encode(map[string]string{"status": "done", "kind": "video"})
		case r.URL.Path == "/v1/jobs/job-9/result":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("text"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer badResult.Close()
	t.Setenv("HUB_MEDIA_URL", badResult.URL)
	if _, err := s.MediaFetch(context.Background(), "job-9", "clip"); err == nil || !strings.Contains(err.Error(), "unsupported media type") {
		t.Fatalf("unsupported result mime: %v", err)
	}
}

func TestServiceGenerateDuplicateAndPromptGuards(t *testing.T) {
	pngBody := pngBytes(t, 1, 1)
	srv, _ := serviceStub(t, "done", pngBody, "image/png")
	s, dir := openSession(t)
	grantFile(t, dir, srv.URL, true)
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")

	if _, err := s.Generate(context.Background(), "dup", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Generate(context.Background(), "dup", "x"); err == nil {
		t.Fatal("duplicate artifact overwritten")
	}
	if _, err := s.VideoGenerate(context.Background(), strings.Repeat("x", 3000)); err == nil {
		t.Fatal("oversized prompt accepted")
	}
	t.Setenv("OPENAI_API_KEY", "supersecretvalue123")
	if _, err := s.VideoGenerate(context.Background(), "draw supersecretvalue123 please"); err == nil {
		t.Fatal("credential material accepted in prompt")
	}
}

func TestServiceDoNoClientAndResult404(t *testing.T) {
	s, _ := openSession(t)
	s.HTTP = nil
	t.Setenv("HUB_MEDIA_URL", "http://x")
	if _, err := s.MediaFetch(context.Background(), "job-1", "clip"); err == nil {
		t.Fatal("missing http client accepted")
	}

	s, dir := openSession(t)
	grantFile(t, dir, "http://127.0.0.1:9", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/result") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "done", "kind": "video"})
	}))
	defer srv.Close()
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	if _, err := s.MediaFetch(context.Background(), "job-1", "clip"); err == nil {
		t.Fatal("404 result accepted")
	}
}

func TestDialImageAndWriteEdges(t *testing.T) {
	// The image URL dialer refuses blocked hosts, private IPs and junk.
	for _, addr := range []string{"localhost:443", "127.0.0.1:443", "10.0.0.5:443", "bad", "x:"} {
		if conn, err := dialImage(context.Background(), "tcp", addr, ""); err == nil {
			if conn != nil {
				conn.Close()
			}
			t.Fatalf("dial accepted %q", addr)
		}
	}
	s, dir := openSession(t)
	writeWorkspace(t, dir, "note.txt", []byte("old"))
	// Replacing a missing file is refused.
	if err := s.writeReplacing("missing.txt", []byte("x")); err == nil {
		t.Fatal("replaced a missing file")
	}
	// Replacing works on an existing regular file.
	if err := s.writeReplacing("note.txt", []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "note.txt"))
	if string(got) != "new" {
		t.Fatalf("replace: %q", got)
	}
	// Oversized service result is refused at the bound.
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(make([]byte, MaxImageBytes+1024))
	}))
	defer big.Close()
	grantFile(t, dir, big.URL, true)
	t.Setenv("HUB_MEDIA_URL", big.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	if _, err := s.Generate(context.Background(), "huge", "x"); err == nil {
		t.Fatal("oversized result accepted")
	}
}

func TestServiceGenerateBadImageBytes(t *testing.T) {
	// Magic bytes sniff as png but the structure is garbage → imageBounds fails.
	garbagePNG := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("junk")...)
	srv, _ := serviceStub(t, "done", garbagePNG, "image/png")
	s, dir := openSession(t)
	grantFile(t, dir, srv.URL, true)
	t.Setenv("HUB_MEDIA_URL", srv.URL)
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	if _, err := s.Generate(context.Background(), "bad", "x"); err == nil {
		t.Fatal("corrupt image accepted")
	}
}

func TestSmallHelpersCoverage(t *testing.T) {
	s := &Session{}
	if s.falBase() != "https://fal.run" {
		t.Fatal("default fal base")
	}
	s.FalBase = "https://fal.example.com"
	if s.falBase() != "https://fal.example.com" {
		t.Fatal("custom fal base")
	}
	if s.configPath() != "/state/hermes/config.yaml" {
		t.Fatal("default config path")
	}
	t.Setenv("HUB_HERMES_CONFIG", "x")
	if s.configPath() != "x" {
		t.Fatal("env config path")
	}
	if defaultImageModel(FalProvider) != ImageModel || defaultImageModel(ProviderCLIProxy) != CLIProxyDefaultModel {
		t.Fatal("default models")
	}
	// readRegular refuses directories and missing files.
	s2, dir := openSession(t)
	writeWorkspace(t, dir, "a.txt", []byte("x"))
	if _, err := s2.readRegular("a.txt", 4); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "adir"), 0o700)
	if _, err := s2.readRegular("adir", 4); err == nil {
		t.Fatal("directory read")
	}
	if _, err := s2.readRegular("missing.txt", 4); err == nil {
		t.Fatal("missing read")
	}
}
