package mediasvc

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComfyWorkflowGeneratesImageFromServiceVolume(t *testing.T) {
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	prompt := "a quoted \"image\"\nwith a second line"
	var submitted, viewed atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unconfigured ComfyUI received unrelated provider credential")
		}
		switch r.URL.Path {
		case "/system_stats":
			_, _ = io.WriteString(w, `{}`)
		case "/prompt":
			if r.Method != http.MethodPost {
				t.Errorf("wrong submission method: %s", r.Method)
			}
			var payload struct {
				Prompt map[string]struct {
					Class  string                     `json:"class_type"`
					Inputs map[string]json.RawMessage `json:"inputs"`
				} `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			var text string
			if err := json.Unmarshal(payload.Prompt["6"].Inputs["text"], &text); err != nil || text != prompt || payload.Prompt["6"].Class != "CLIPTextEncode" {
				t.Error("workflow prompt was corrupted or not injected into its input")
			}
			submitted.Add(1)
			_, _ = io.WriteString(w, `{"prompt_id":"prompt-1"}`)
		case "/history/prompt-1":
			_, _ = io.WriteString(w, `{"prompt-1":{"outputs":{"9":{"images":[{"filename":"fixture.png","subfolder":"","type":"output"}]}},"status":{"completed":true,"status_str":"success"}}}`)
		case "/view":
			if r.URL.Query().Get("filename") != "fixture.png" || r.URL.Query().Get("type") != "output" {
				t.Error("wrong ComfyUI output reference")
			}
			viewed.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(picture.Bytes())
		default:
			t.Errorf("unexpected ComfyUI route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	workflow := `{"6":{"class_type":"CLIPTextEncode","inputs":{"text":"${PROMPT}"}},"9":{"class_type":"SaveImage","inputs":{"images":["6",0]}}}`
	if err := os.WriteFile(filepath.Join(dir, "portrait.json"), []byte(workflow), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MEDIA_ENGINE", "comfy")
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_UPSTREAM", upstream.URL)
	t.Setenv("HUB_MEDIA_MODEL_DIR", dir)
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	c, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := genEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	if !engine.Ready(t.Context()) {
		t.Error("configured ComfyUI health probe failed")
	}
	result, err := engine.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: prompt})
	if err != nil || result.Mime != "image/png" || !bytes.Equal(result.Data, picture.Bytes()) || submitted.Load() != 1 || viewed.Load() != 1 {
		t.Fatalf("local workflow contract failed: mime=%s submits=%d downloads=%d err=%v", result.Mime, submitted.Load(), viewed.Load(), err)
	}
}

func TestComfyGenerateWaitsForQueuedWorkflowWithoutResubmitting(t *testing.T) {
	var submissions, histories atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prompt":
			submissions.Add(1)
			_, _ = io.WriteString(w, `{"prompt_id":"prompt-1"}`)
		case "/history/prompt-1":
			if histories.Add(1) == 1 {
				_, _ = io.WriteString(w, `{}`)
			} else {
				_, _ = io.WriteString(w, `{"prompt-1":{"outputs":{"9":{"images":[{"filename":"fixture.png","type":"output"}]}}}}`)
			}
		case "/view":
			_, _ = w.Write(testPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portrait.json"), []byte(`{"6":{"inputs":{"text":"${PROMPT}"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	engine, err := genEngine(Config{Role: RoleMedia, Engine: "comfy", Upstream: upstream.URL, ModelDir: dir, JobTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "fixture"})
	if err != nil || !bytes.Equal(result.Data, testPNG) || submissions.Load() != 1 || histories.Load() != 2 {
		t.Fatalf("queue polling failed: submits=%d polls=%d err=%v", submissions.Load(), histories.Load(), err)
	}
}

func TestComfyGenerateHonorsJobDeadline(t *testing.T) {
	var submissions, histories atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/prompt" {
			submissions.Add(1)
			_, _ = io.WriteString(w, `{"prompt_id":"prompt-1"}`)
			return
		}
		histories.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portrait.json"), []byte(`{"6":{"inputs":{"text":"${PROMPT}"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	engine, err := genEngine(Config{Role: RoleMedia, Engine: "comfy", Upstream: upstream.URL, ModelDir: dir, JobTimeout: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = engine.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "deadline") || time.Since(started) > time.Second || histories.Load() != 1 || submissions.Load() != 1 {
		t.Fatalf("job deadline ignored: submits=%d polls=%d elapsed=%s err=%v", submissions.Load(), histories.Load(), time.Since(started), err)
	}
}

func TestComfyUsesOnlyItsExplicitProviderCredential(t *testing.T) {
	var authorized atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer comfy-fixture" {
			http.Error(w, "missing scoped credential", http.StatusUnauthorized)
			return
		}
		authorized.Add(1)
		switch r.URL.Path {
		case "/system_stats":
			_, _ = io.WriteString(w, `{}`)
		case "/prompt":
			_, _ = io.WriteString(w, `{"prompt_id":"prompt-1"}`)
		case "/history/prompt-1":
			_, _ = io.WriteString(w, `{"prompt-1":{"outputs":{"9":{"images":[{"filename":"fixture.png","type":"output"}]}}}}`)
		case "/view":
			_, _ = w.Write(testPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portrait.json"), []byte(`{"6":{"inputs":{"text":"${PROMPT}"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMFY_FIXTURE_KEY", "comfy-fixture")
	t.Setenv("OPENAI_API_KEY", "foreign-fixture")
	engine, err := genEngine(Config{Role: RoleMedia, Engine: "comfy", ModelDir: dir, Providers: map[string]MediaProvider{"comfy": {Upstream: upstream.URL, KeyEnv: "COMFY_FIXTURE_KEY"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !engine.Ready(t.Context()) {
		t.Error("scoped ComfyUI readiness denied")
	}
	result, err := engine.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "fixture"})
	if err != nil || !bytes.Equal(result.Data, testPNG) || authorized.Load() != 4 {
		t.Fatalf("ComfyUI credential missing or foreign: authorized=%d err=%v", authorized.Load(), err)
	}
}

func TestComfyVideoResumesPersistedHandleWithoutSubmission(t *testing.T) {
	var submitted, viewed atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prompt":
			submitted.Add(1)
			http.Error(w, "must not resubmit", http.StatusInternalServerError)
		case "/history/prompt-1":
			_, _ = io.WriteString(w, `{"prompt-1":{"outputs":{"9":{"images":[{"filename":"fixture.mp4","type":"output"}],"animated":[true]}},"status":{"status_str":"success","completed":true}}}`)
		case "/view":
			viewed.Add(1)
			_, _ = w.Write(testMP4)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	store, err := newJobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(&Job{ID: "job-comfy-resume", Status: "running", Kind: "video", Model: "clip", RemoteID: "prompt-1", Engine: "comfy", ProviderModel: "video-workflow"}); err != nil {
		t.Fatal(err)
	}
	_, err = Serve(Config{Role: RoleMedia, Engine: "comfy", Upstream: upstream.URL, DataDir: dir, ModelDir: t.TempDir(), Auth: "fixture", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.get("job-comfy-resume")
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "done" {
			data, err := os.ReadFile(filepath.Join(store.dirOf(job.ID), "result.bin"))
			if err != nil || job.Mime != "video/mp4" || !bytes.Equal(data, testMP4) || submitted.Load() != 0 || viewed.Load() != 1 {
				t.Fatalf("resume output invalid: submits=%d downloads=%d mime=%s err=%v", submitted.Load(), viewed.Load(), job.Mime, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("persisted ComfyUI video did not resume")
}

func TestComfyWorkflowCannotEscapeServiceVolume(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "workflows")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "private.json")
	if err := os.WriteFile(outside, []byte(`{"6":{"inputs":{"text":"${PROMPT}"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"prompt_id":"must-not-submit"}`)
	}))
	defer upstream.Close()
	engine, err := genEngine(Config{Engine: "comfy", Upstream: upstream.URL, ModelDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"../private", strings.TrimSuffix(outside, ".json")}
	if err := os.Symlink(outside, filepath.Join(directory, "shortcut.json")); err == nil {
		names = append(names, "shortcut")
	} else {
		t.Log("symlink creation unavailable; traversal and absolute-path checks still run")
	}
	for _, name := range names {
		_, err := engine.(AsyncEngine).Submit(t.Context(), GenRequest{Model: name, Prompt: "fixture"})
		if err == nil || !strings.Contains(err.Error(), "workflow") || calls.Load() != 0 {
			t.Errorf("workflow escaped service volume: calls=%d err=%v", calls.Load(), err)
		}
	}
}

func comfyProtocolFixture(t *testing.T, history string, views *atomic.Int32) (AsyncEngine, *atomic.Int32) {
	t.Helper()
	histories := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/history/prompt-1":
			histories.Add(1)
			_, _ = io.WriteString(w, history)
		case "/view":
			views.Add(1)
			_, _ = w.Write(testPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("HUB_MEDIA_ENGINE", "comfy")
	t.Setenv("HUB_MEDIA_ENGINES", "")
	t.Setenv("HUB_MEDIA_PROVIDERS", "")
	t.Setenv("HUB_MEDIA_AUTH", "fixture")
	t.Setenv("HUB_MEDIA_UPSTREAM", upstream.URL)
	t.Setenv("HUB_MEDIA_MODEL_DIR", t.TempDir())
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	c, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := genEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	async, ok := engine.(AsyncEngine)
	if !ok {
		t.Fatal("ComfyUI lacks durable Submit/Poll interface")
	}
	return async, histories
}

func TestComfyPendingHistoryDoesNotFetchOrComplete(t *testing.T) {
	var views atomic.Int32
	engine, histories := comfyProtocolFixture(t, `{}`, &views)
	result, done, err := engine.Poll(t.Context(), SubmitHandle{RemoteID: "prompt-1", Engine: "comfy", Model: "portrait"}, "portrait")
	if err != nil || done || len(result.Data) != 0 || views.Load() != 0 || histories.Load() != 1 {
		t.Fatalf("pending workflow handled incorrectly: done=%t views=%d histories=%d err=%v", done, views.Load(), histories.Load(), err)
	}
}

func TestComfyFailedWorkflowNeverDownloadsAnOutput(t *testing.T) {
	var views atomic.Int32
	engine, histories := comfyProtocolFixture(t, `{"prompt-1":{"status":{"status_str":"error"},"outputs":{"9":{"images":[{"filename":"partial.png","type":"output"}]}}}}`, &views)
	result, done, err := engine.Poll(t.Context(), SubmitHandle{RemoteID: "prompt-1", Engine: "comfy", Model: "portrait"}, "portrait")
	if err == nil || done || len(result.Data) != 0 || views.Load() != 0 || histories.Load() != 1 {
		t.Fatalf("failed workflow not checked: done=%t views=%d histories=%d err=%v", done, views.Load(), histories.Load(), err)
	}
}

func TestComfyAmbiguousSavedOutputsFailClosed(t *testing.T) {
	var views atomic.Int32
	engine, histories := comfyProtocolFixture(t, `{"prompt-1":{"status":{"status_str":"success"},"outputs":{"9":{"images":[{"filename":"first.png","type":"output"},{"filename":"second.png","type":"output"}]}}}}`, &views)
	result, done, err := engine.Poll(t.Context(), SubmitHandle{RemoteID: "prompt-1", Engine: "comfy", Model: "portrait"}, "portrait")
	if err == nil || done || len(result.Data) != 0 || views.Load() != 0 || histories.Load() != 1 {
		t.Fatalf("ambiguous workflow not checked: done=%t views=%d histories=%d err=%v", done, views.Load(), histories.Load(), err)
	}
}

func TestComfyUnsafeEndpointConfigurationIsRejected(t *testing.T) {
	for _, endpoint := range []string{"", "file:///tmp/model", "https://user:fixture-secret@provider.invalid", "https://provider.invalid?secret=x", "https://provider.invalid#x", "http:///missing-host"} {
		_, err := genEngine(Config{Role: RoleMedia, Engine: "comfy", Upstream: endpoint, ModelDir: t.TempDir()})
		if err == nil {
			t.Error("unsafe ComfyUI endpoint accepted")
		}
		if err != nil && strings.Contains(err.Error(), "fixture-secret") {
			t.Error("endpoint error leaked an inline credential")
		}
	}
}

func TestComfyRedirectCannotReachAnotherService(t *testing.T) {
	var redirectCalls, foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer foreign.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectCalls.Add(1)
		http.Redirect(w, r, foreign.URL, http.StatusFound)
	}))
	defer upstream.Close()
	engine, err := genEngine(Config{Role: RoleMedia, Engine: "comfy", Upstream: upstream.URL, ModelDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if ready := engine.Ready(t.Context()); ready || redirectCalls.Load() != 1 || foreignCalls.Load() != 0 {
		t.Fatalf("readiness redirect escaped fixed endpoint: ready=%t checked=%d foreign=%d", ready, redirectCalls.Load(), foreignCalls.Load())
	}
}

func TestComfyInvalidOrOversizedWorkflowNeverSubmits(t *testing.T) {
	var submits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submits.Add(1)
		_, _ = io.WriteString(w, `{"prompt_id":"prompt-1"}`)
	}))
	defer upstream.Close()
	large, _ := json.Marshal(map[string]any{"6": map[string]any{"inputs": map[string]any{"text": "${PROMPT}", "extra": strings.Repeat("x", 1<<20)}}})
	dir := t.TempDir()
	for _, body := range []string{`{`, `{"6":{"inputs":42}}`, `{"6":{"inputs":{"text":"no marker"}}}`, string(large)} {
		if err := os.WriteFile(filepath.Join(dir, "portrait.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		engine, err := genEngine(Config{Role: RoleMedia, Engine: "comfy", Upstream: upstream.URL, ModelDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		async, ok := engine.(AsyncEngine)
		if !ok {
			t.Fatal("missing durable ComfyUI interface")
		}
		_, err = async.Submit(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "fixture"})
		if err == nil || !strings.Contains(err.Error(), "workflow") || submits.Load() != 0 {
			t.Errorf("unsafe workflow reached submission or lacked a configuration error: submits=%d err=%v", submits.Load(), err)
		}
	}
}

func TestComfyInvalidMediaOutputIsBounded(t *testing.T) {
	var views atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/history/prompt-1" {
			_, _ = io.WriteString(w, `{"prompt-1":{"status":{"status_str":"success"},"outputs":{"9":{"images":[{"filename":"fixture.png","type":"output"}]}}}}`)
			return
		}
		views.Add(1)
		if r.URL.Query().Get("filename") != "fixture.png" {
			t.Error("incorrect output reference")
		}
		_, _ = io.WriteString(w, "this is not an image")
	}))
	defer upstream.Close()
	engine, err := genEngine(Config{Role: RoleMedia, Engine: "comfy", Upstream: upstream.URL, ModelDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	result, done, err := engine.(AsyncEngine).Poll(t.Context(), SubmitHandle{RemoteID: "prompt-1", Engine: "comfy"}, "portrait")
	if err == nil || !strings.Contains(err.Error(), "media") || done || len(result.Data) != 0 || views.Load() != 1 {
		t.Fatalf("invalid output accepted or not checked: done=%t views=%d err=%v", done, views.Load(), err)
	}
}

func TestComfyOversizedOutputIsRejected(t *testing.T) {
	var views atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/history/prompt-1" {
			_, _ = io.WriteString(w, `{"prompt-1":{"outputs":{"9":{"images":[{"filename":"large.png","type":"output"}]}}}}`)
			return
		}
		views.Add(1)
		chunk := make([]byte, 1<<20)
		for n := 0; n < 49; n++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	engine, err := genEngine(Config{Role: RoleMedia, Engine: "comfy", Upstream: upstream.URL, ModelDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	result, done, err := engine.(AsyncEngine).Poll(t.Context(), SubmitHandle{RemoteID: "prompt-1", Engine: "comfy"}, "portrait")
	if err == nil || !strings.Contains(err.Error(), "bound") || done || len(result.Data) != 0 || views.Load() != 1 {
		t.Fatalf("oversized output not bounded: done=%t views=%d err=%v", done, views.Load(), err)
	}
}
