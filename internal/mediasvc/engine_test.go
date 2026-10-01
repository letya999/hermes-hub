package mediasvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestConfigFromEnvValidation(t *testing.T) {
	// Role must be one of the two valid values.
	setEnv(t, map[string]string{
		"HUB_MEDIA_ROLE": "nope", "HUB_MEDIA_AUTH": "x", "HUB_MEDIA_ENGINE": "command", "HUB_MEDIA_COMMAND": "y",
	})
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid role accepted")
	}
	// Auth is mandatory.
	setEnv(t, map[string]string{"HUB_MEDIA_ROLE": "stt", "HUB_MEDIA_AUTH": ""})
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("missing auth accepted")
	}
	// Remote engine requires an upstream.
	setEnv(t, map[string]string{
		"HUB_MEDIA_ROLE": "stt", "HUB_MEDIA_AUTH": "x", "HUB_MEDIA_ENGINE": "remote", "HUB_MEDIA_UPSTREAM": "",
	})
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("remote engine without upstream accepted")
	}
	// Command engine requires a command.
	setEnv(t, map[string]string{
		"HUB_MEDIA_ROLE": "stt", "HUB_MEDIA_AUTH": "x", "HUB_MEDIA_ENGINE": "command", "HUB_MEDIA_COMMAND": "",
	})
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("command engine without command accepted")
	}
	// Unknown engine refused.
	setEnv(t, map[string]string{
		"HUB_MEDIA_ROLE": "stt", "HUB_MEDIA_AUTH": "x", "HUB_MEDIA_ENGINE": "wat",
	})
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("unknown engine accepted")
	}
	// Sherpa without the native build refuses.
	setEnv(t, map[string]string{
		"HUB_MEDIA_ROLE": "stt", "HUB_MEDIA_AUTH": "x", "HUB_MEDIA_ENGINE": "sherpa",
		"HUB_MEDIA_MODEL_DIR": t.TempDir(), "HUB_MEDIA_DATA": t.TempDir(),
	})
	if _, err := ConfigFromEnv(); err != nil {
		t.Fatalf("config parse: %v", err)
	}
	if _, err := sttEngine(Config{Engine: "sherpa"}); err == nil {
		t.Fatal("sherpa engine available without the native build")
	}
	if _, err := ttsEngine(Config{Engine: "sherpa"}); err == nil {
		t.Fatal("sherpa tts available without the native build")
	}
	// Full valid config parses all knobs.
	setEnv(t, map[string]string{
		"HUB_MEDIA_ROLE": "tts", "HUB_MEDIA_AUTH": "x", "HUB_MEDIA_ENGINE": "command",
		"HUB_MEDIA_COMMAND": "/bin/tts", "HUB_MEDIA_WORKERS": "4", "HUB_MEDIA_JOB_TTL": "2h",
		"HUB_MEDIA_FETCH_HOSTS": "a.com, b.com", "HUB_MEDIA_LISTEN": ":9999",
	})
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.Role != "tts" || c.Workers != 4 || c.JobTTL != 2*time.Hour || len(c.FetchHosts) != 2 || c.ListenAddr != ":9999" {
		t.Fatalf("config fields: %+v", c)
	}
}

func scriptFile(t *testing.T, name, sh, cmd string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		path += ".cmd"
		if err := os.WriteFile(path, []byte(cmd), 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(path, []byte(sh), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestCommandEngines(t *testing.T) {
	ctx := context.Background()
	stt := scriptFile(t, "stt",
		"#!/bin/sh\necho hello-transcript\n",
		"@echo off\r\necho hello-transcript\r\n")
	segs, err := (commandSTT{command: stt}).Transcribe(ctx, "x.wav", TranscribeOpts{})
	if err != nil || len(segs) != 1 || segs[0].Text != "hello-transcript" {
		t.Fatalf("command stt: %+v %v", segs, err)
	}
	if !(commandSTT{command: stt}).Ready(ctx) {
		t.Fatal("command stt not ready")
	}
	bad := scriptFile(t, "badstt", "#!/bin/sh\nexit 1\n", "@echo off\r\nexit /b 1\r\n")
	if _, err := (commandSTT{command: bad}).Transcribe(ctx, "x.wav", TranscribeOpts{}); err == nil {
		t.Fatal("failing command transcribed")
	}

	tts := scriptFile(t, "tts",
		"#!/bin/sh\ncat >/dev/null\nprintf audio-bytes\n",
		"@echo off\r\nmore\r\necho audio-bytes\r\n")
	audio, err := (commandTTS{command: tts}).Synthesize(ctx, "hi", "")
	if err != nil || len(audio) == 0 {
		t.Fatalf("command tts: %v", err)
	}
	if !(commandTTS{command: tts}).Ready(ctx) {
		t.Fatal("command tts not ready")
	}
	if _, err := (commandTTS{command: bad}).Synthesize(ctx, "hi", ""); err == nil {
		t.Fatal("failing command synthesized")
	}
}

func TestRemoteEnginesAgainstUpstream(t *testing.T) {
	var sawKey bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("Authorization") == "Bearer key"
		switch r.URL.Path {
		case "/v1/audio/transcriptions":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"text": "всё вместе",
				"segments": []map[string]any{
					{"start": 0.0, "end": 1.0, "text": " часть один "},
					{"start": 1.0, "end": 2.0, "text": " часть два"},
				},
			})
		case "/v1/audio/speech":
			_, _ = w.Write([]byte("upstream-audio"))
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	ctx := context.Background()

	cfg := Config{Engine: "remote", Upstream: upstream.URL, UpstreamKey: "key"}
	stt, err := sttEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wav := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(wav, writeTestWav(t, 0.5), 0600); err != nil {
		t.Fatal(err)
	}
	segs, err := stt.Transcribe(ctx, wav, TranscribeOpts{Language: "ru"})
	if err != nil || len(segs) != 2 || segs[0].Text != "часть один" || segs[0].End != 1.0 {
		t.Fatalf("remote stt: %+v %v", segs, err)
	}
	if !sawKey {
		t.Fatal("upstream did not see the bearer key")
	}
	if !stt.Ready(ctx) {
		t.Fatal("remote stt unready against live upstream")
	}

	tts, err := ttsEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	audio, err := tts.Synthesize(ctx, "привет", "voice-x")
	if err != nil || string(audio) != "upstream-audio" {
		t.Fatalf("remote tts: %v", err)
	}
	if !tts.Ready(ctx) {
		t.Fatal("remote tts unready")
	}

	// A dead upstream reports unready and fails calls.
	dead := remoteSTT{base: "http://127.0.0.1:1", client: &http.Client{Timeout: time.Second}}
	if dead.Ready(ctx) {
		t.Fatal("dead upstream reported ready")
	}
	if _, err := dead.Transcribe(ctx, wav, TranscribeOpts{}); err == nil {
		t.Fatal("dead upstream transcribed")
	}
	// Non-200 upstream fails the call.
	errUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errUp.Close()
	if _, err := (remoteSTT{base: errUp.URL, client: http.DefaultClient}).Transcribe(ctx, wav, TranscribeOpts{}); err == nil {
		t.Fatal("500 upstream transcribed")
	}
	if _, err := (remoteTTS{base: errUp.URL, client: http.DefaultClient}).Synthesize(ctx, "x", ""); err == nil {
		t.Fatal("500 upstream synthesized")
	}
}

func TestEngineSelectionAndMaterialize(t *testing.T) {
	if _, err := sttEngine(Config{Engine: "bogus"}); err == nil {
		t.Fatal("bogus stt engine accepted")
	}
	if _, err := ttsEngine(Config{Engine: "bogus"}); err == nil {
		t.Fatal("bogus tts engine accepted")
	}
	s := newTestService(t, testConfig(t))
	// materialize dispatch: url vs path vs nothing.
	if _, err := s.materialize(context.Background(), "", "", t.TempDir()); err == nil {
		t.Fatal("empty source materialized")
	}
	if _, err := s.materialize(context.Background(), "", "x.wav", t.TempDir()); err == nil {
		t.Fatal("path source without input root accepted")
	}
	// A real uploaded file is reused as-is.
	up := filepath.Join(t.TempDir(), "up.bin")
	if err := os.WriteFile(up, []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "upload.bin")
	if err := os.WriteFile(src, []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.jobSource(context.Background(), &Job{}, dir)
	if err != nil || got != src {
		t.Fatalf("jobSource upload: %q %v", got, err)
	}
}

func TestJobListAndSweep(t *testing.T) {
	s := newTestService(t, testConfig(t))
	// list returns jobs oldest-first; sweep removes expired finished work.
	old := &Job{ID: "job-old", Status: "done", CreatedAt: time.Now().Add(-48 * time.Hour)}
	old.UpdatedAt = time.Now().Add(-48 * time.Hour)
	live := &Job{ID: "job-live", Status: "queued", CreatedAt: time.Now()}
	for _, j := range []*Job{live, old} {
		if err := s.jobs.put(j); err != nil {
			t.Fatal(err)
		}
	}
	// put persists UpdatedAt; force the old one back in time for the sweep.
	j, err := s.jobs.get("job-old")
	if err != nil {
		t.Fatal(err)
	}
	j.UpdatedAt = time.Now().Add(-48 * time.Hour)
	b, _ := json.Marshal(j)
	if err := os.WriteFile(filepath.Join(s.jobs.dirOf("job-old"), "job.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	list := s.jobs.list()
	if len(list) != 2 || list[0].ID != "job-old" {
		t.Fatalf("list order: %v", list)
	}
	s.jobs.sweep(time.Hour)
	if _, err := s.jobs.get("job-old"); !os.IsNotExist(err) {
		t.Fatal("expired job survived sweep")
	}
	if _, err := s.jobs.get("job-live"); err != nil {
		t.Fatal("live job swept")
	}
}

func TestServeRequeuesOrphanedJobs(t *testing.T) {
	cfg := testConfig(t)
	cfg.Engine = "command"
	cfg.Command = "true"
	// A job orphaned in "running" must come back "queued" after Serve().
	store, _ := newJobStore(cfg.DataDir)
	j := &Job{ID: "job-orphan", Status: "running", CreatedAt: time.Now()}
	if err := store.put(j); err != nil {
		t.Fatal(err)
	}
	h, err := Serve(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := store.get("job-orphan"); err == nil && got.Status != "running" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := store.get("job-orphan")
	t.Fatalf("orphan job never requeued: %s", got.Status)
}

func TestLongAudioChunksAndOffsets(t *testing.T) {
	cfg := testConfig(t)
	s := newTestService(t, cfg)
	s.stt = &fakeSTT{ready: true, segments: []Segment{{Start: 0, End: 1, Text: "chunk"}}}
	// 65 s of wav -> three 30 s windows; wavSlice stubbed to a real short wav.
	src := writeTestWav(t, 65)
	part := writeTestWav(t, 1)
	old := wavSliceFn
	wavSliceFn = func(_ context.Context, _, dst string, _, _ float64) error {
		return os.WriteFile(dst, part, 0600)
	}
	t.Cleanup(func() { wavSliceFn = old })
	wav := filepath.Join(t.TempDir(), "long.wav")
	if err := os.WriteFile(wav, src, 0600); err != nil {
		t.Fatal(err)
	}
	segs, err := s.transcribeWav(context.Background(), wav, TranscribeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 3 {
		t.Fatalf("expected 3 chunk segments, got %d", len(segs))
	}
	for i, sg := range segs {
		want := float64(i) * sttChunkSeconds
		if sg.Start != want {
			t.Fatalf("chunk %d start %v, want %v", i, sg.Start, want)
		}
	}
}

func TestJobSourceFromRequestJSON(t *testing.T) {
	cfg := testConfig(t)
	root := t.TempDir()
	cfg.InputRoot = root
	s := newTestService(t, cfg)
	in := filepath.Join(root, "call.ogg")
	if err := os.WriteFile(in, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := s.jobs.dirOf("job-src")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(jobRequest{SourcePath: "call.ogg"})
	if err := os.WriteFile(filepath.Join(dir, "request.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.jobSource(context.Background(), &Job{}, dir)
	if err != nil || got != in {
		t.Fatalf("jobSource path: %q %v", got, err)
	}
	// Extension helper keeps sane names for fetched files.
	if extensionFromResponse(&http.Response{}, mustURL("https://x/a.wav")) != ".wav" {
		t.Fatal("ext lost")
	}
}

func mustURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}

func TestDecodeAndSliceWithRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.wav")
	if err := os.WriteFile(src, writeTestWav(t, 2), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.wav")
	if err := decodeAudio(ctx, src, out); err != nil {
		t.Fatal(err)
	}
	if dur, err := wavDuration(out); err != nil || dur < 1.8 {
		t.Fatalf("decoded duration %v %v", dur, err)
	}
	part := filepath.Join(dir, "part.wav")
	if err := wavSlice(ctx, src, part, 0.5, 1.0); err != nil {
		t.Fatal(err)
	}
	if dur, err := wavDuration(part); err != nil || dur < 0.8 || dur > 1.3 {
		t.Fatalf("sliced duration %v %v", dur, err)
	}
	if err := decodeAudio(ctx, filepath.Join(dir, "none"), out); err == nil {
		t.Fatal("missing input decoded")
	}
}

func TestSpeechOpusTranscodeWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	cfg := testConfig(t)
	cfg.Role = RoleTTS
	s := newTestService(t, cfg)
	s.tts = &fakeTTS{ready: true, audio: writeTestWav(t, 0.5)}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/speech",
		strings.NewReader(`{"input":"привет","response_format":"opus"}`))
	authReq(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("opus speech: %d", resp.StatusCode)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("OggS")) && !bytes.HasPrefix(buf.Bytes(), []byte("RIFF")) {
		t.Fatalf("unexpected audio: %q", buf.Bytes()[:4])
	}
}

func TestTranscriptionErrorPaths(t *testing.T) {
	stubDecode(t)
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true, err: errTest}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	// Engine failure -> 502.
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", "v.wav")
	part.Write(writeTestWav(t, 0.5))
	form.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/transcriptions", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("engine failure: %d", resp.StatusCode)
	}
	// Missing file field -> 400.
	body.Reset()
	form = multipart.NewWriter(&body)
	form.Close()
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/transcriptions", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing file: %d", resp.StatusCode)
	}
	// Non-multipart body -> 400.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/transcriptions", strings.NewReader("x"))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-multipart: %d", resp.StatusCode)
	}
}

var errTest = errors.New("engine down")

func TestJobsListEndpointAndDeleteConflict(t *testing.T) {
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	j := &Job{ID: "job-run", Status: "running", CreatedAt: time.Now()}
	if err := s.jobs.put(j); err != nil {
		t.Fatal(err)
	}
	// GET list shows the job.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs", nil)
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	var list map[string]any
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list["jobs"].([]any)) != 1 {
		t.Fatalf("jobs list: %v", list)
	}
	// Running job refuses deletion and unfinished result.
	for _, path := range []string{"/v1/jobs/job-run", "/v1/jobs/job-run/result"} {
		req, _ = http.NewRequest(http.MethodDelete, srv.URL+path, nil)
		if strings.HasSuffix(path, "result") {
			req, _ = http.NewRequest(http.MethodGet, srv.URL+path, nil)
		}
		authReq(req)
		resp, _ = http.DefaultClient.Do(req)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	// Unknown job -> 404.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/job-none", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job: %d", resp.StatusCode)
	}
}

func TestJobFailureAndResultFormats(t *testing.T) {
	stubDecode(t)
	s := newTestService(t, testConfig(t))
	// Engine down -> the job lands in "failed" with a recorded error.
	s.stt = &fakeSTT{ready: true, err: errTest}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", "a.wav")
	part.Write(writeTestWav(t, 0.5))
	form.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	var created struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+created.ID, nil)
		authReq(req)
		resp, _ := http.DefaultClient.Do(req)
		var j map[string]any
		json.NewDecoder(resp.Body).Decode(&j)
		resp.Body.Close()
		if j["status"] == "failed" {
			if j["error"] == "" || j["error"] == nil {
				t.Fatal("failed job lost its error")
			}
			break
		}
		if j["status"] == "done" {
			t.Fatal("job succeeded against a down engine")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A done job serves txt/json/srt results.
	s.stt = &fakeSTT{ready: true, segments: []Segment{{Start: 0, End: 1, Text: "готово"}}}
	var body2 bytes.Buffer
	form = multipart.NewWriter(&body2)
	part, _ = form.CreateFormFile("file", "b.wav")
	part.Write(writeTestWav(t, 0.5))
	form.Close()
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", &body2)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, _ := s.jobs.get(created.ID)
		if j != nil && j.Status == "done" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, format := range []string{"txt", "json", "srt"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+created.ID+"/result?format="+format, nil)
		authReq(req)
		resp, _ := http.DefaultClient.Do(req)
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(buf.String(), "готово") {
			t.Fatalf("result %s: %d %q", format, resp.StatusCode, buf.String())
		}
	}
	// Corrupt job.json -> not found, not a crash.
	bad := s.jobs.dirOf("job-bad")
	os.MkdirAll(bad, 0700)
	os.WriteFile(filepath.Join(bad, "job.json"), []byte("{"), 0600)
	if _, err := s.jobs.get("job-bad"); err == nil {
		t.Fatal("corrupt job loaded")
	}
}

func TestJobStorePutErrorsAndOversizeUpload(t *testing.T) {
	// put fails when the job dir path is occupied by a regular file.
	dir := t.TempDir()
	store, err := newJobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(store.dirOf("job-block"))
	if err := os.MkdirAll(filepath.Dir(blocker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.put(&Job{ID: "job-block"}); err == nil {
		t.Fatal("put succeeded into a file-blocked dir")
	}
	// Uploads above MaxSource are refused before a job is created.
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", "huge.wav")
	part.Write(make([]byte, testConfig(t).MaxSource+1024))
	form.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: %d", resp.StatusCode)
	}
	// Unsupported method on the jobs root.
	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/v1/jobs", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("put jobs: %d", resp.StatusCode)
	}
	// Unknown sub-path under a job id.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/job-x/odd", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("odd path: %d", resp.StatusCode)
	}
}

func TestMiscBranches(t *testing.T) {
	ctx := context.Background()
	// runJob early-returns for a non-queued or missing job.
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true}
	done := &Job{ID: "job-done", Status: "done", CreatedAt: time.Now()}
	if err := s.jobs.put(done); err != nil {
		t.Fatal(err)
	}
	s.runJob(ctx, "job-done") // must not re-run or corrupt the record
	if j, _ := s.jobs.get("job-done"); j.Status != "done" {
		t.Fatal("done job re-ran")
	}
	s.runJob(ctx, "job-missing") // missing job: silent return, no panic

	// A queued job whose request.json names a url source fails cleanly when
	// remote fetch is unconfigured — the error lands on the job record.
	dir := s.jobs.dirOf("job-urlsrc")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(jobRequest{SourceURL: "https://files.example.com/a.wav"})
	if err := os.WriteFile(filepath.Join(dir, "request.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	j := &Job{ID: "job-urlsrc", Status: "queued", CreatedAt: time.Now()}
	if err := s.jobs.put(j); err != nil {
		t.Fatal(err)
	}
	s.runJob(ctx, "job-urlsrc")
	if got, _ := s.jobs.get("job-urlsrc"); got.Status != "failed" || got.Error == "" {
		t.Fatalf("url-source job: %s %q", got.Status, got.Error)
	}

	// TTS-role healthz reflects its engine, and /v1/jobs refuses when stt is
	// absent.
	cfg := testConfig(t)
	cfg.Role = RoleTTS
	ts := newTestService(t, cfg)
	ts.tts = &fakeTTS{ready: true}
	srv := httptest.NewServer(ts.routes())
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/healthz")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal("tts healthz failed")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("jobs on tts role: %d", resp.StatusCode)
	}
	// Sync transcription without the file part and without multipart at all.
	srv2 := httptest.NewServer(s.routes())
	defer srv2.Close()
	req, _ = http.NewRequest(http.MethodPost, srv2.URL+"/v1/audio/transcriptions", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty transcription body: %d", resp.StatusCode)
	}
}

func TestSaveFetchBodyBounds(t *testing.T) {
	s := newTestService(t, testConfig(t))
	dir := t.TempDir()
	u, _ := url.Parse("https://files.example.com/meeting.mp3")

	// Happy path: bytes land in a fresh file with the source extension.
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("audio-bytes")),
	}
	got, err := s.saveFetchBody(resp, u, dir)
	if err != nil || filepath.Ext(got) != ".mp3" {
		t.Fatalf("saveFetchBody: %q %v", got, err)
	}
	if b, _ := os.ReadFile(got); string(b) != "audio-bytes" {
		t.Fatalf("content: %q", b)
	}
	// Non-200 is refused.
	resp2 := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(""))}
	if _, err := s.saveFetchBody(resp2, u, t.TempDir()); err == nil {
		t.Fatal("non-200 accepted")
	}
	// Oversize body refused and the partial file removed.
	resp3 := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(s.cfg.MaxSource)+8)))}
	if _, err := s.saveFetchBody(resp3, u, t.TempDir()); err == nil {
		t.Fatal("oversize body accepted")
	}
	// Empty body refused.
	resp4 := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}
	if _, err := s.saveFetchBody(resp4, u, t.TempDir()); err == nil {
		t.Fatal("empty body accepted")
	}
}

func TestWavDurationAndTranscriptText(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(f, writeTestWav(t, 2), 0600); err != nil {
		t.Fatal(err)
	}
	dur, err := wavDuration(f)
	if err != nil || dur < 1.9 || dur > 2.1 {
		t.Fatalf("duration %v %v", dur, err)
	}
	if _, err := wavDuration(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("missing wav measured")
	}
	if _, err := wavDuration(f + ".bad"); err == nil {
		os.WriteFile(f+".bad", []byte("nope"), 0600)
		if _, err := wavDuration(f + ".bad"); err == nil {
			t.Fatal("non-wav measured")
		}
	}
	joined := transcriptText([]Segment{
		{Text: "один", Speaker: "speaker-0"},
		{Text: "два", Speaker: "speaker-0"},
		{Text: "три", Speaker: "speaker-1"},
		{Text: "четыре"},
	})
	if !strings.Contains(joined, "speaker-0: один два") || !strings.Contains(joined, "speaker-1: три") {
		t.Fatalf("transcriptText grouping: %q", joined)
	}
	if got := transcriptText(nil); got != "" {
		t.Fatalf("empty transcript: %q", got)
	}
	if !strings.Contains(toSRT([]Segment{{Start: 1, End: 2.5, Text: "x", Speaker: "speaker-9"}}), "[speaker-9]") {
		t.Fatal("srt lost speaker prefix")
	}
}
