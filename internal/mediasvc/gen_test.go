package mediasvc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
var testMP4 = []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 1, 2}

type fakeGen struct {
	result   GenResult
	err      error
	ready    bool
	lastReq  GenRequest
	submits  atomic.Int32
	polls    atomic.Int32
	pollDone bool
}

func (f *fakeGen) Generate(_ context.Context, req GenRequest) (GenResult, error) {
	f.lastReq = req
	if f.err != nil {
		return GenResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeGen) Submit(_ context.Context, req GenRequest) (string, error) {
	f.submits.Add(1)
	return "req-1", nil
}

func (f *fakeGen) Poll(context.Context, string, string) (GenResult, bool, error) {
	f.polls.Add(1)
	return f.result, f.pollDone, f.err
}

func (f *fakeGen) Ready(context.Context) bool { return f.ready }

// fakeGenNoAsync satisfies GenEngine only — no AsyncEngine surface.
type fakeGenNoAsync struct{}

func (f *fakeGenNoAsync) Generate(context.Context, GenRequest) (GenResult, error) {
	return GenResult{}, errors.New("sync only")
}
func (f *fakeGenNoAsync) Ready(context.Context) bool { return true }

func mediaConfig(t *testing.T) Config {
	t.Helper()
	cfg := testConfig(t)
	cfg.Role = RoleMedia
	cfg.ImageModel = "gpt-image-2"
	cfg.ImageModels = []string{"gpt-image-2"}
	cfg.VideoModel = "fal-ai/test-video"
	cfg.VideoModels = []string{"fal-ai/test-video"}
	cfg.JobTimeout = 10 * time.Second
	return cfg
}

func newMediaService(t *testing.T, gen GenEngine) *service {
	t.Helper()
	s := newTestService(t, mediaConfig(t))
	s.gen = gen
	return s
}

func TestMediaRoleAuthRequired(t *testing.T) {
	s := newMediaService(t, &fakeGen{ready: true})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits", "/v1/jobs", "/v1/jobs/job-x", "/v1/models"} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
			t.Fatalf("%s answered without auth", path)
		}
	}
}

func TestImageGenerationContract(t *testing.T) {
	gen := &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}}
	s := newMediaService(t, gen)
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations",
		strings.NewReader(`{"model":"gpt-image-2","prompt":"a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, testPNG) {
		t.Fatalf("generation: %d %s %dB", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	}
	if gen.lastReq.Model != "gpt-image-2" || gen.lastReq.Prompt != "a cat" {
		t.Fatalf("engine request: %+v", gen.lastReq)
	}
}

func TestImageGenerationChatModelAllowed(t *testing.T) {
	gen := &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}}
	s := newMediaService(t, gen)
	s.cfg.ChatModels = []string{"gemini-2.5-flash-image"}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations",
		strings.NewReader(`{"model":"gemini-2.5-flash-image","prompt":"a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gen.lastReq.Model != "gemini-2.5-flash-image" {
		t.Fatalf("chat model: %d %+v", resp.StatusCode, gen.lastReq)
	}
}

func TestImageGenerationValidation(t *testing.T) {
	gen := &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}}
	s := newMediaService(t, gen)
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"prompt":"x","model":"evil-model"}`, http.StatusBadRequest},
		{`{"model":"gpt-image-2"}`, http.StatusBadRequest},
		{`{"model":"gpt-image-2","prompt":"` + strings.Repeat("x", 3000) + `"}`, http.StatusBadRequest},
		{`{bad`, http.StatusBadRequest},
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		authReq(req)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: %d", tc.body, resp.StatusCode)
		}
	}
	// Empty model falls back to the configured default.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gen.lastReq.Model != "gpt-image-2" {
		t.Fatalf("default model: %d %+v", resp.StatusCode, gen.lastReq)
	}
}

func TestImageEditContract(t *testing.T) {
	gen := &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}}
	s := newMediaService(t, gen)
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("image", "in.png")
	part.Write(testPNG)
	form.WriteField("model", "gpt-image-2")
	form.WriteField("prompt", "make it blue")
	form.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/edits", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(out, testPNG) {
		t.Fatalf("edit: %d %dB", resp.StatusCode, len(out))
	}
	if !bytes.Equal(gen.lastReq.Source, testPNG) || gen.lastReq.SourceMime != "image/png" {
		t.Fatalf("edit source not forwarded: %+v", gen.lastReq)
	}

	// A non-image upload is refused before reaching the engine.
	body.Reset()
	form = multipart.NewWriter(&body)
	part, _ = form.CreateFormFile("image", "in.bin")
	part.Write([]byte("not an image"))
	form.WriteField("prompt", "x")
	form.Close()
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/images/edits", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-image upload: %d", resp.StatusCode)
	}
}

func TestVideoJobLifecycle(t *testing.T) {
	gen := &fakeGen{result: GenResult{Data: testMP4, Mime: "video/mp4"}, pollDone: true}
	s := newMediaService(t, gen)
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs",
		strings.NewReader(`{"kind":"video","prompt":"a rocket launch"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || !validJobID(created.ID) {
		t.Fatalf("job create: %d %+v", resp.StatusCode, created)
	}

	deadline := time.Now().Add(5 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+created.ID, nil)
		authReq(req)
		resp, _ := http.DefaultClient.Do(req)
		last = map[string]any{}
		json.NewDecoder(resp.Body).Decode(&last)
		resp.Body.Close()
		if last["status"] == "done" || last["status"] == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last["status"] != "done" {
		t.Fatalf("job never finished: %v", last)
	}
	if last["kind"] != "video" {
		t.Fatalf("kind lost: %v", last)
	}
	if gen.submits.Load() != 1 {
		t.Fatalf("submit count %d", gen.submits.Load())
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+created.ID+"/result", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp4" || !bytes.Equal(out, testMP4) {
		t.Fatalf("result: %d %s %dB", resp.StatusCode, resp.Header.Get("Content-Type"), len(out))
	}
}

func TestVideoJobResumesRemoteID(t *testing.T) {
	gen := &fakeGen{result: GenResult{Data: testMP4, Mime: "video/mp4"}, pollDone: true}
	s := newMediaService(t, gen)
	// A job persisted mid-flight keeps its provider handle; a restart must
	// resume polling instead of paying for a second submission.
	j := &Job{ID: "job-resume", Status: "queued", Kind: "video", Model: "fal-ai/test-video", Prompt: "x", RemoteID: "req-99"}
	if err := s.jobs.put(j); err != nil {
		t.Fatal(err)
	}
	s.runJob(context.Background(), j.ID)
	if gen.submits.Load() != 0 {
		t.Fatalf("resumed job re-submitted")
	}
	stored, err := s.jobs.get("job-resume")
	if err != nil || stored.Status != "done" {
		t.Fatalf("resumed job: %+v %v", stored, err)
	}
}

func TestVideoJobRequiresAsyncEngine(t *testing.T) {
	s := newMediaService(t, &fakeGenNoAsync{})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", strings.NewReader(`{"kind":"video","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-async engine accepted job: %d", resp.StatusCode)
	}
}

func TestMediaModelsEndpoint(t *testing.T) {
	s := newMediaService(t, &fakeGen{ready: true})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	var out struct {
		ImageModels []string `json:"image_models"`
		VideoModels []string `json:"video_models"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.ImageModels) != 1 || out.ImageModels[0] != "gpt-image-2" || len(out.VideoModels) != 1 {
		t.Fatalf("models: %+v", out)
	}
}

// ---- engines against httptest upstreams

func TestRemoteEngineGenerations(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("path %q", r.URL.Path)
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "gpt-image-2" || req["response_format"] != "b64_json" {
			t.Fatalf("upstream request: %v", req)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Fatal("upstream key missing")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(testPNG)}},
		})
	}))
	defer upstream.Close()
	eng := &remoteGen{base: upstream.URL, keyFor: staticKey("k"), client: upstream.Client()}
	out, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "gpt-image-2", Prompt: "cat"})
	if err != nil || !bytes.Equal(out.Data, testPNG) || out.Mime != "image/png" {
		t.Fatalf("generate: %v %+v", err, out)
	}
}

func TestRemoteEngineEdits(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" || !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			t.Fatalf("edit request: %q %q", r.URL.Path, r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		f, hdr, err := r.FormFile("image")
		if err != nil || hdr.Filename != "in.png" {
			t.Fatalf("image field: %v", hdr)
		}
		f.Close()
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(testPNG)}},
		})
	}))
	defer upstream.Close()
	eng := &remoteGen{base: upstream.URL, keyFor: staticKey("k"), client: upstream.Client()}
	out, err := eng.Generate(context.Background(), GenRequest{
		Kind: "image", Model: "gpt-image-2", Prompt: "blue",
		Source: testPNG, SourceName: "in.png",
	})
	if err != nil || !bytes.Equal(out.Data, testPNG) {
		t.Fatalf("edit: %v", err)
	}
}

func TestFalEngineSyncAndQueue(t *testing.T) {
	var submitted, statusChecks atomic.Int32
	queue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/fal-ai/test-video":
			submitted.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"request_id": "req-42"})
		case strings.HasSuffix(r.URL.Path, "/requests/req-42/status"):
			statusChecks.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
		case strings.HasSuffix(r.URL.Path, "/requests/req-42"):
			json.NewEncoder(w).Encode(map[string]any{"video": map[string]string{"url": "https://v3.fal.media/files/x.mp4"}})
		default:
			t.Fatalf("queue path %q", r.URL.Path)
		}
	}))
	defer queue.Close()

	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Key fal-key" {
			t.Fatal("fal key missing")
		}
		if r.URL.Path != "/fal-ai/flux" {
			t.Fatalf("fal path %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"images": []map[string]string{{"url": "https://v3.fal.media/files/i.png"}},
		})
	}))
	defer base.Close()

	eng := &falGen{base: base.URL, queue: queue.URL, keyFor: staticKey("fal-key"), client: base.Client()}
	eng.fetch = func(_ context.Context, raw string) ([]byte, string, error) {
		if !strings.HasPrefix(raw, "https://v3.fal.media/") {
			return nil, "", errors.New("unexpected fetch url")
		}
		return testPNG, "image/png", nil
	}
	out, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "fal-ai/flux", Prompt: "cat"})
	if err != nil || !bytes.Equal(out.Data, testPNG) {
		t.Fatalf("fal generate: %v", err)
	}
	// Edit is refused for fal.
	if _, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "m", Prompt: "x", Source: testPNG}); err == nil {
		t.Fatal("fal edit accepted")
	}

	id, err := eng.Submit(context.Background(), GenRequest{Kind: "video", Model: "fal-ai/test-video", Prompt: "rocket"})
	if err != nil || id != "req-42" {
		t.Fatalf("submit: %v %q", err, id)
	}
	eng.fetch = func(_ context.Context, raw string) ([]byte, string, error) {
		return testMP4, "video/mp4", nil
	}
	res, done, err := eng.Poll(context.Background(), id, "fal-ai/test-video")
	if err != nil || !done || res.Mime != "video/mp4" || !bytes.Equal(res.Data, testMP4) {
		t.Fatalf("poll: %v done=%v %+v", err, done, res)
	}
}

func TestMediaConfigValidation(t *testing.T) {
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	t.Setenv("HUB_MEDIA_ENGINE", "remote")
	t.Setenv("HUB_MEDIA_UPSTREAM", "http://x")
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "")
	if _, err := ConfigFromEnv(RoleMedia); err == nil {
		t.Fatal("media without image model accepted")
	}
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "gpt-image-2")
	cfg, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ImageModels) != 1 || cfg.ImageModels[0] != "gpt-image-2" {
		t.Fatalf("image allowlist: %v", cfg.ImageModels)
	}
	t.Setenv("HUB_MEDIA_ENGINE", "command")
	if _, err := ConfigFromEnv(RoleMedia); err == nil {
		t.Fatal("command engine accepted for media")
	}
	t.Setenv("HUB_MEDIA_ENGINE", "fal")
	os.Unsetenv("FAL_KEY")
	os.Unsetenv("HUB_MEDIA_UPSTREAM_KEY")
	if _, err := ConfigFromEnv(RoleMedia); err == nil {
		t.Fatal("fal without key accepted")
	}
}

// ---- coverage for engine plumbing: selection, chat-image path, result fetch

func TestGenEngineSelection(t *testing.T) {
	cfg := mediaConfig(t)
	cfg.Engine = "remote"
	cfg.Upstream = "http://up"
	cfg.UpstreamKey = "k"
	cfg.ChatModels = []string{"gemini-image"}
	eng, err := genEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	remote, ok := eng.(*remoteGen)
	if !ok || !remote.chatModels["gemini-image"] {
		t.Fatalf("remote engine: %#v", eng)
	}
	cfg.Engine = "fal"
	cfg.Upstream = "https://fal.run"
	eng, err = genEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fal, ok := eng.(*falGen)
	falKey, _ := fal.apiKey(context.Background())
	if !ok || fal.queue != "https://queue.fal.run" || falKey != "k" {
		t.Fatalf("fal engine: %#v", eng)
	}
	cfg.Engine = "bogus"
	if _, err := genEngine(cfg); err == nil {
		t.Fatal("unknown engine accepted")
	}
	if got := falQueueBase("https://example.com"); got != "" {
		t.Fatalf("non-fal host produced a queue base: %s", got)
	}
	if got := falQueueBase("https://fal.run/"); got != "https://queue.fal.run" {
		t.Fatalf("queue base: %s", got)
	}
	if !toSet([]string{"a", " b "})["b"] {
		t.Fatal("toSet did not trim")
	}
}

func TestRemoteChatImagePath(t *testing.T) {
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNG)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path %q", r.URL.Path)
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["modalities"] == nil {
			t.Fatalf("chat request missing modalities: %v", req)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"images": []map[string]any{{"image_url": map[string]string{"url": dataURI}}},
			}}},
		})
	}))
	defer upstream.Close()
	eng := &remoteGen{base: upstream.URL, keyFor: staticKey("k"), chatModels: map[string]bool{"gemini-image": true}, client: upstream.Client()}
	out, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "gemini-image", Prompt: "cat", Source: testPNG, SourceMime: "image/png"})
	if err != nil || !bytes.Equal(out.Data, testPNG) || out.Mime != "image/png" {
		t.Fatalf("chat image: %v %+v", err, out)
	}
}

func TestDecodeDataImageEdges(t *testing.T) {
	if _, _, err := decodeDataImage("https://x/i.png"); err == nil {
		t.Fatal("non-data uri accepted")
	}
	if _, _, err := decodeDataImage("data:image/png"); err == nil {
		t.Fatal("no comma accepted")
	}
	if _, _, err := decodeDataImage("data:image/png;base64,!!!"); err == nil {
		t.Fatal("bad base64 accepted")
	}
	data, mime, err := decodeDataImage("data:;base64," + base64.RawStdEncoding.EncodeToString(testPNG))
	if err != nil || len(data) != len(testPNG) || mime != "image/png" {
		t.Fatalf("raw base64: %v %s", err, mime)
	}
}

func TestSniffAndMimeAllowlist(t *testing.T) {
	for mime, data := range map[string][]byte{
		"image/png":  testPNG,
		"image/jpeg": {0xff, 0xd8, 0xff, 1},
		"image/webp": {'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'},
		"video/mp4":  testMP4,
		"video/webm": {0x1a, 0x45, 0xdf, 0xa3},
	} {
		if got := sniffMime(data); got != mime || !mediaMimeOK(got) {
			t.Fatalf("%s: got %s", mime, got)
		}
	}
	if sniffMime([]byte("plain text")) != "application/octet-stream" {
		t.Fatal("unknown bytes misidentified")
	}
	if mediaMimeOK("application/octet-stream") || mediaMimeOK("text/html") {
		t.Fatal("unsafe mime allowed")
	}
}

func TestFetchResultGuards(t *testing.T) {
	ctx := context.Background()
	// No allowlist: refused outright.
	if _, _, err := fetchResult(ctx, "https://fal.media/f.png", nil, 1<<20); err == nil {
		t.Fatal("fetch without allowlist")
	}
	hosts := []string{"fal.media"}
	// http and non-allowlisted hosts are refused before any dial.
	for _, u := range []string{"http://fal.media/f.png", "https://evil.example.com/f.png", "notaurl"} {
		if _, _, err := fetchResult(ctx, u, hosts, 1<<20); err == nil {
			t.Fatalf("fetch accepted %q", u)
		}
	}
	// Allowlisted but resolves nowhere/public-check catches loopback via DNS.
	if _, _, err := fetchResult(ctx, "https://localhost.invalid/f.png", []string{"localhost.invalid"}, 1<<20); err == nil {
		t.Fatal("private host accepted")
	}
}

func TestFalPollTransitions(t *testing.T) {
	var checks atomic.Int32
	queue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			if checks.Add(1) == 1 {
				json.NewEncoder(w).Encode(map[string]string{"status": "IN_PROGRESS"})
			} else {
				json.NewEncoder(w).Encode(map[string]string{"status": "COMPLETED"})
			}
		case strings.HasSuffix(r.URL.Path, "/req-7"):
			json.NewEncoder(w).Encode(map[string]any{"images": []map[string]string{{"url": "https://v3.fal.media/x.png"}}})
		default:
			t.Fatalf("queue path %q", r.URL.Path)
		}
	}))
	defer queue.Close()
	eng := &falGen{base: "https://fal.run", queue: queue.URL, keyFor: staticKey("k"), client: queue.Client()}
	eng.fetch = func(context.Context, string) ([]byte, string, error) {
		return testPNG, "image/png", nil
	}
	res, done, err := eng.Poll(context.Background(), "req-7", "m")
	if err != nil || done {
		t.Fatalf("in-progress: %v %v", err, done)
	}
	res, done, err = eng.Poll(context.Background(), "req-7", "m")
	if err != nil || !done || !bytes.Equal(res.Data, testPNG) {
		t.Fatalf("completed: %v done=%v", err, done)
	}
	// An unknown queue status is terminal.
	eng2 := &falGen{base: "https://fal.run", queue: queue.URL, keyFor: staticKey("k"), client: queue.Client()}
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "CANCELLED"})
	}))
	defer dead.Close()
	eng2.queue = dead.URL
	if _, _, err := eng2.Poll(context.Background(), "req-7", "m"); err == nil {
		t.Fatal("terminal queue status accepted")
	}
}

func TestMediaJobPendingResultIs409(t *testing.T) {
	s := newMediaService(t, &fakeGen{})
	if err := s.jobs.put(&Job{ID: "job-pend", Status: "queued", Kind: "video"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/job-pend/result", nil)
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pending result: %d", resp.StatusCode)
	}
	// Deleting a queued job works.
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/v1/jobs/job-pend", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestMediaJobRejectsBadKind(t *testing.T) {
	s := newMediaService(t, &fakeGen{})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	for _, body := range []string{
		`{"kind":"image","prompt":"x"}`,
		`{"kind":"video"}`,
		`{"kind":"video","prompt":"x","model":"not-allowed"}`,
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		authReq(req)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, resp.StatusCode)
		}
	}
}

func TestImagesEditsRequiresFile(t *testing.T) {
	s := newMediaService(t, &fakeGen{})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("prompt", "x")
	form.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/edits", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("edit without image: %d", resp.StatusCode)
	}
}

func TestWriteMediaRejectsUnsafeMime(t *testing.T) {
	s := newMediaService(t, &fakeGen{})
	rec := httptest.NewRecorder()
	s.writeMedia(rec, GenResult{Data: []byte("<html>hi</html>"), Mime: "text/html"})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unsafe mime served: %d", rec.Code)
	}
}

func TestEngineReadiness(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()
	remote := &remoteGen{base: upstream.URL, keyFor: staticKey("k"), client: upstream.Client()}
	if !remote.Ready(context.Background()) {
		t.Fatal("remote engine unready against live upstream")
	}
	down := &remoteGen{base: "http://127.0.0.1:1", client: upstream.Client()}
	if down.Ready(context.Background()) {
		t.Fatal("remote engine ready against dead upstream")
	}
	fal := &falGen{keyFor: staticKey("k")}
	if !fal.Ready(context.Background()) {
		t.Fatal("fal unready with key")
	}
	fal.keyFor = staticKey("")
	if fal.Ready(context.Background()) {
		t.Fatal("fal ready without key")
	}
}

func TestRunGenJobFailurePaths(t *testing.T) {
	// Poll error fails the job durably.
	gen := &fakeGen{err: errors.New("provider exploded")}
	s := newMediaService(t, gen)
	j := &Job{ID: "job-err", Status: "queued", Kind: "video", Model: "fal-ai/test-video", Prompt: "x", RemoteID: "req-9"}
	if err := s.jobs.put(j); err != nil {
		t.Fatal(err)
	}
	s.runGenJob(context.Background(), j)
	stored, err := s.jobs.get("job-err")
	if err != nil || stored.Status != "failed" || !strings.Contains(stored.Error, "exploded") {
		t.Fatalf("failed job: %+v %v", stored, err)
	}
	// A completed provider job with no bytes fails explicitly.
	gen2 := &fakeGen{result: GenResult{}, pollDone: true}
	s2 := newMediaService(t, gen2)
	j2 := &Job{ID: "job-empty", Status: "queued", Kind: "video", Model: "fal-ai/test-video", Prompt: "x", RemoteID: "req-1"}
	if err := s2.jobs.put(j2); err != nil {
		t.Fatal(err)
	}
	s2.runGenJob(context.Background(), j2)
	stored, _ = s2.jobs.get("job-empty")
	if stored.Status != "failed" || !strings.Contains(stored.Error, "no media") {
		t.Fatalf("empty result: %+v", stored)
	}
	// Timeout: JobTimeout=0 maps to the default here; use an already-cancelled
	// parent context to exercise the timeout branch cheaply.
	gen3 := &fakeGen{result: GenResult{Data: testMP4, Mime: "video/mp4"}, pollDone: false}
	s3 := newMediaService(t, gen3)
	j3 := &Job{ID: "job-timeout", Status: "queued", Kind: "video", Model: "fal-ai/test-video", Prompt: "x", RemoteID: "req-2"}
	if err := s3.jobs.put(j3); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s3.runGenJob(ctx, j3)
	stored, _ = s3.jobs.get("job-timeout")
	if stored.Status != "failed" {
		t.Fatalf("timeout job: %+v", stored)
	}
}

func TestEngineErrorBranches(t *testing.T) {
	// remote: upstream 500 and malformed payloads both surface as errors.
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()
	eng := &remoteGen{base: fail.URL, client: fail.Client()}
	if _, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("upstream 500 accepted")
	}
	if _, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "m", Prompt: "x", Source: testPNG}); err == nil {
		t.Fatal("edit upstream 500 accepted")
	}
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"url":"https://x"}]}`)) // no b64_json
	}))
	defer garbage.Close()
	eng = &remoteGen{base: garbage.URL, client: garbage.Client()}
	if _, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("payload without image accepted")
	}
	emptyChat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer emptyChat.Close()
	eng = &remoteGen{base: emptyChat.URL, chatModels: map[string]bool{"cm": true}, client: emptyChat.Client()}
	if _, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "cm", Prompt: "x"}); err == nil {
		t.Fatal("empty chat choices accepted")
	}
	// fal: submit/result error surfaces.
	badQueue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			json.NewEncoder(w).Encode(map[string]string{"status": "COMPLETED"})
			return
		}
		if strings.Contains(r.URL.Path, "/requests/") {
			w.Write([]byte(`{}`)) // result document with no media
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badQueue.Close()
	fal := &falGen{base: fail.URL, queue: badQueue.URL, keyFor: staticKey("k"), client: fail.Client()}
	if _, err := fal.Generate(context.Background(), GenRequest{Kind: "image", Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("fal 500 accepted")
	}
	if _, err := fal.Submit(context.Background(), GenRequest{Kind: "video", Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("submit 500 accepted")
	}
	if _, done, err := fal.Poll(context.Background(), "req-1", "m"); err == nil || done {
		t.Fatal("result without media accepted")
	}
	// fal without a queue base refuses video outright.
	noQueue := &falGen{base: "https://fal.run", keyFor: staticKey("k")}
	if _, err := noQueue.Submit(context.Background(), GenRequest{Kind: "video", Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("submit without queue base accepted")
	}
}

func TestServeMediaRole(t *testing.T) {
	cfg := mediaConfig(t)
	cfg.Engine = "remote"
	cfg.Upstream = "http://127.0.0.1:9"
	cfg.ListenAddr = "127.0.0.1:0"
	h, err := Serve(cfg)
	if err != nil || h == nil {
		t.Fatalf("serve media: %v", err)
	}
	cfg.Engine = "fal"
	cfg.Upstream = "https://fal.run"
	cfg.UpstreamKey = "k"
	if _, err := Serve(cfg); err != nil {
		t.Fatalf("serve fal: %v", err)
	}
}

func TestGenJobSubmitFailure(t *testing.T) {
	gen := &fakeGen{err: errors.New("submit exploded")}
	s := newMediaService(t, gen)
	j := &Job{ID: "job-subfail", Status: "queued", Kind: "video", Model: "fal-ai/test-video", Prompt: "x"}
	if err := s.jobs.put(j); err != nil {
		t.Fatal(err)
	}
	s.runGenJob(context.Background(), j)
	stored, _ := s.jobs.get("job-subfail")
	if stored.Status != "failed" || !strings.Contains(stored.Error, "submit exploded") {
		t.Fatalf("submit failure: %+v", stored)
	}
}

func TestImagesEditsMultipartErrors(t *testing.T) {
	s := newMediaService(t, &fakeGen{})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	// Non-multipart body is refused.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/edits", strings.NewReader(`{"prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-multipart edit: %d", resp.StatusCode)
	}
	// Engine failure is a 502, not a panic.
	s.gen = &fakeGen{err: errors.New("provider down")}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("image", "in.png")
	part.Write(testPNG)
	form.WriteField("prompt", "x")
	form.Close()
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/images/edits", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("engine error: %d", resp.StatusCode)
	}
}

func TestFalPollStatusHTTPError(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer dead.Close()
	eng := &falGen{base: "https://fal.run", queue: dead.URL, keyFor: staticKey("k"), client: dead.Client()}
	if _, _, err := eng.Poll(context.Background(), "req-1", "m"); err == nil {
		t.Fatal("queue 502 accepted")
	}
}

func TestFetchResultUnset(t *testing.T) {
	eng := &falGen{base: "https://fal.run", keyFor: staticKey("k")}
	if _, err := eng.fetchResult(context.Background(), "https://x"); err == nil {
		t.Fatal("fetch without hook accepted")
	}
}

func TestMediaConfigVideoAndListJobs(t *testing.T) {
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	t.Setenv("HUB_MEDIA_ENGINE", "remote")
	t.Setenv("HUB_MEDIA_UPSTREAM", "http://up/")
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "gpt-image-2")
	t.Setenv("HUB_MEDIA_VIDEO_MODEL", "fal-ai/veo3")
	t.Setenv("HUB_MEDIA_QUEUE_UPSTREAM", "https://queue.example.com/")
	t.Setenv("HUB_MEDIA_WORKERS", "0")
	cfg, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers != 2 || cfg.Upstream != "http://up" || cfg.QueueUpstream != "https://queue.example.com" {
		t.Fatalf("config normalize: %+v", cfg)
	}
	if len(cfg.VideoModels) != 1 || cfg.VideoModels[0] != "fal-ai/veo3" {
		t.Fatalf("video allowlist: %v", cfg.VideoModels)
	}

	s := newMediaService(t, &fakeGen{ready: true})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	// healthz reflects the media engine.
	resp, _ := http.Get(srv.URL + "/healthz")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("media healthz")
	}
	// Job listing is part of the contract.
	if err := s.jobs.put(&Job{ID: "job-list", Status: "queued", Kind: "video"}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	var listing struct {
		Jobs []map[string]any `json:"jobs"`
	}
	json.NewDecoder(resp.Body).Decode(&listing)
	resp.Body.Close()
	if len(listing.Jobs) != 1 || listing.Jobs[0]["kind"] != "video" {
		t.Fatalf("jobs list: %+v", listing)
	}
	// Speech surfaces are closed on the media role — auth alone is not enough.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/transcriptions", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("media role served the stt endpoint")
	}
}

func TestNewJobStoreBadDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newJobStore(filepath.Join(blocker, "sub")); err == nil {
		t.Fatal("job store created under a file")
	}
}

func TestMediaEdgeBranches(t *testing.T) {
	s := newMediaService(t, &fakeGen{err: errors.New("provider down")})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	// Engine failure is a 502.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("engine error: %d", resp.StatusCode)
	}
	// Edit path model allowlist.
	s.gen = &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("image", "in.png")
	part.Write(testPNG)
	form.WriteField("model", "not-allowed")
	form.WriteField("prompt", "x")
	form.Close()
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/images/edits", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("edit bad model: %d", resp.StatusCode)
	}
	// Multipart job post on the media role is invalid JSON → 400.
	body.Reset()
	form = multipart.NewWriter(&body)
	form.WriteField("kind", "video")
	form.Close()
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("multipart job: %d", resp.StatusCode)
	}
	// A running job cannot be deleted.
	if err := s.jobs.put(&Job{ID: "job-run", Status: "running", Kind: "video"}); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/v1/jobs/job-run", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("running delete: %d", resp.StatusCode)
	}
	// Failed job status exposes its error.
	if err := s.jobs.put(&Job{ID: "job-fail", Status: "failed", Kind: "video", Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/job-fail", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	var j map[string]any
	json.NewDecoder(resp.Body).Decode(&j)
	resp.Body.Close()
	if j["status"] != "failed" || j["error"] != "boom" {
		t.Fatalf("failed job: %v", j)
	}
}

func TestServeRejectsSpeechRoles(t *testing.T) {
	cfg := mediaConfig(t)
	cfg.Role = RoleTTS
	cfg.Engine = "command"
	cfg.Command = "true"
	cfg.ListenAddr = "127.0.0.1:0"
	if _, err := Serve(cfg); err != nil {
		t.Fatalf("serve tts command: %v", err)
	}
	cfg.Engine = "nope"
	if _, err := Serve(cfg); err == nil {
		t.Fatal("tts accepted unknown engine")
	}
}

func TestFalGenerateNoImageAndOversizedEdit(t *testing.T) {
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"images": []map[string]string{}})
	}))
	defer base.Close()
	eng := &falGen{base: base.URL, keyFor: staticKey("k"), client: base.Client()}
	if _, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("fal response without image accepted")
	}

	s := newMediaService(t, &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("image", "big.png")
	part.Write(make([]byte, 17<<20)) // past the 16 MiB edit-source cap
	form.WriteField("prompt", "x")
	form.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/edits", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("oversized edit source accepted")
	}
}

func TestMediaEndpointEdgeCases(t *testing.T) {
	s := newMediaService(t, &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	// Unknown method on a job item → 405.
	if err := s.jobs.put(&Job{ID: "job-405", Status: "done", Kind: "video"}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/jobs/job-405", nil)
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("put: %d", resp.StatusCode)
	}
	// Oversized size hint is refused.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations",
		strings.NewReader(`{"prompt":"x","size":"`+strings.Repeat("9", 40)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("size: %d", resp.StatusCode)
	}
	// Unknown job id → 404.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/job-nope", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing job: %d", resp.StatusCode)
	}
	// Chat-image model without a source goes text-only.
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		msgs := req["messages"].([]any)
		if _, isString := msgs[0].(map[string]any)["content"].(string); !isString {
			t.Fatalf("expected text-only content: %v", msgs[0])
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"images": []map[string]any{{"image_url": map[string]string{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNG)}}},
			}}},
		})
	}))
	defer chat.Close()
	eng := &remoteGen{base: chat.URL, chatModels: map[string]bool{"cm": true}, client: chat.Client()}
	out, err := eng.Generate(context.Background(), GenRequest{Kind: "image", Model: "cm", Prompt: "x"})
	if err != nil || out.Mime != "image/png" {
		t.Fatalf("chat image: %v", err)
	}
}
