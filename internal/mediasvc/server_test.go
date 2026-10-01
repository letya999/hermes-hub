package mediasvc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSTT struct {
	segments []Segment
	err      error
	ready    bool
	calls    int
}

func (f *fakeSTT) Transcribe(context.Context, string, TranscribeOpts) ([]Segment, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.segments, nil
}

func (f *fakeSTT) Ready(context.Context) bool { return f.ready }

type fakeTTS struct {
	audio []byte
	err   error
	ready bool
}

func (f *fakeTTS) Synthesize(context.Context, string, string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.audio, nil
}

func (f *fakeTTS) Ready(context.Context) bool { return f.ready }

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Role: RoleSTT, Auth: "tok", DataDir: t.TempDir(),
		Workers: 2, Engine: "command", Command: "true",
		JobTTL: time.Hour, MaxUpload: 1 << 20, MaxSource: 1 << 20,
	}
}

func newTestService(t *testing.T, cfg Config) *service {
	t.Helper()
	store, err := newJobStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	return &service{cfg: cfg, jobs: store, sem: make(chan struct{}, cfg.Workers)}
}

// writeTestWav emits a canonical 16 kHz mono PCM wav of `seconds` duration.
func writeTestWav(t *testing.T, seconds float64) []byte {
	t.Helper()
	rate := 16000
	n := int(float64(rate) * seconds)
	pcm := make([]byte, n*2)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

func stubDecode(t *testing.T) {
	t.Helper()
	old := decodeAudioFn
	decodeAudioFn = func(_ context.Context, src, dst string) error {
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0600)
	}
	t.Cleanup(func() { decodeAudioFn = old })
}

func authReq(req *http.Request) { req.Header.Set("Authorization", "Bearer tok") }

func TestSyncTranscriptionFormats(t *testing.T) {
	stubDecode(t)
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true, segments: []Segment{{Start: 0, End: 1.5, Text: "привет мир"}}}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	for _, format := range []string{"text", "json", "verbose_json", "srt"} {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, _ := form.CreateFormFile("file", "voice.wav")
		part.Write(writeTestWav(t, 1))
		form.WriteField("response_format", format)
		form.Close()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/transcriptions", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		authReq(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		payload := make([]byte, 4096)
		n, _ := resp.Body.Read(payload)
		resp.Body.Close()
		out := string(payload[:n])
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d body %s", format, resp.StatusCode, out)
		}
		if !strings.Contains(out, "привет мир") && !strings.Contains(out, "привет") {
			t.Fatalf("%s: transcript missing in %q", format, out)
		}
	}
	if format := "srt"; true {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, _ := form.CreateFormFile("file", "voice.wav")
		part.Write(writeTestWav(t, 1))
		form.WriteField("response_format", format)
		form.Close()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/transcriptions", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		authReq(req)
		resp, _ := http.DefaultClient.Do(req)
		payload := make([]byte, 4096)
		n, _ := resp.Body.Read(payload)
		resp.Body.Close()
		if !strings.Contains(string(payload[:n]), " --> ") {
			t.Fatalf("srt missing timestamps: %q", payload[:n])
		}
	}
}

func TestAuthRequiredEverywhere(t *testing.T) {
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	for _, path := range []string{"/v1/audio/transcriptions", "/v1/audio/speech", "/v1/jobs", "/v1/jobs/job-x", "/v1/voices"} {
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

func TestAsyncJobLifecycleAndResults(t *testing.T) {
	stubDecode(t)
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true, segments: []Segment{
		{Start: 0, End: 1, Text: "один", Speaker: "speaker-0"},
		{Start: 1, End: 2, Text: "два", Speaker: "speaker-1"},
	}}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", "meeting.wav")
	part.Write(writeTestWav(t, 2))
	form.WriteField("diarize", "true")
	form.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	authReq(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || !validJobID(created.ID) {
		t.Fatalf("job create: %d %+v", resp.StatusCode, created)
	}

	deadline := time.Now().Add(5 * time.Second)
	var done bool
	var last map[string]any
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+created.ID, nil)
		authReq(req)
		resp, _ := http.DefaultClient.Do(req)
		var j map[string]any
		json.NewDecoder(resp.Body).Decode(&j)
		resp.Body.Close()
		last = j
		if j["status"] == "done" {
			done = true
			if !strings.Contains(j["text"].(string), "speaker-0") {
				t.Fatalf("speaker labels lost: %v", j["text"])
			}
			break
		}
		if j["status"] == "failed" {
			t.Fatalf("job failed: %v", j["error"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !done {
		dirEntries, _ := os.ReadDir(s.jobs.dirOf(created.ID))
		names := []string{}
		for _, e := range dirEntries {
			names = append(names, e.Name())
		}
		t.Fatalf("job never finished: last=%v files=%v", last, names)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+created.ID+"/result?format=srt", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.Contains(buf.String(), "[speaker-0]") {
		t.Fatalf("srt missing speaker: %q", buf.String())
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/v1/jobs/"+created.ID, nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestLocalSourceTraversalGuards(t *testing.T) {
	cfg := testConfig(t)
	root := t.TempDir()
	cfg.InputRoot = root
	os.WriteFile(filepath.Join(root, "call.ogg"), []byte("audio"), 0600)
	s := newTestService(t, cfg)
	s.stt = &fakeSTT{ready: true}

	if _, err := s.resolveInput("call.ogg"); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	for _, bad := range []string{"../escape", "..", "sub/../../escape", "/abs/path", "nonexistent.wav"} {
		if _, err := s.resolveInput(bad); err == nil {
			t.Fatalf("traversal accepted: %q", bad)
		}
	}
	// A symlink pointing outside the root must be refused.
	outside := filepath.Join(t.TempDir(), "secret.wav")
	os.WriteFile(outside, []byte("x"), 0600)
	link := filepath.Join(root, "link.wav")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := s.resolveInput("link.wav"); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
	// Oversized input must be refused.
	big := filepath.Join(root, "big.bin")
	os.WriteFile(big, make([]byte, cfg.MaxSource+1), 0600)
	if _, err := s.resolveInput("big.bin"); err == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestRemoteFetchRefusesUnsafeSources(t *testing.T) {
	cfg := testConfig(t)
	s := newTestService(t, cfg)
	s.stt = &fakeSTT{ready: true}
	dir := t.TempDir()
	ctx := context.Background()

	// Fetch disabled entirely without an allowlist.
	if _, err := s.fetchURL(ctx, "https://files.example.com/a.wav", dir); err == nil {
		t.Fatal("fetch succeeded without allowlist")
	}
	s.cfg.FetchHosts = []string{"files.example.com"}
	// Plain http is refused.
	if _, err := s.fetchURL(ctx, "http://files.example.com/a.wav", dir); err == nil {
		t.Fatal("http source accepted")
	}
	// Non-allowlisted host refused.
	if _, err := s.fetchURL(ctx, "https://evil.example.com/a.wav", dir); err == nil {
		t.Fatal("non-allowlisted host accepted")
	}
	// Private targets refused even when the host name is allowlisted.
	s.cfg.FetchHosts = []string{"127.0.0.1", "localhost"}
	for _, u := range []string{"https://127.0.0.1/x.wav", "https://localhost/x.wav"} {
		if _, err := s.fetchURL(ctx, u, dir); err == nil {
			t.Fatalf("private target accepted: %s", u)
		}
	}
}

func TestSpeechEndpointAndVoices(t *testing.T) {
	cfg := testConfig(t)
	cfg.Role = RoleTTS
	cfg.Voice = "ru-RU-dmitri"
	s := newTestService(t, cfg)
	s.tts = &fakeTTS{ready: true, audio: writeTestWav(t, 0.5)}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/speech",
		strings.NewReader(`{"input":"привет","voice":"ru","response_format":"wav"}`))
	authReq(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(buf.Bytes(), []byte("RIFF")) {
		t.Fatalf("speech: %d %q", resp.StatusCode, buf.Bytes()[:16])
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/voices", nil)
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	var v map[string]any
	json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if v["default"] != "ru-RU-dmitri" {
		t.Fatalf("voices: %v", v)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/audio/speech", strings.NewReader(`{"input":""}`))
	authReq(req)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty input: %d", resp.StatusCode)
	}
}

func TestHealthzReflectsEngine(t *testing.T) {
	s := newTestService(t, testConfig(t))
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	s.stt = &fakeSTT{ready: false}
	resp, _ := http.Get(srv.URL + "/healthz")
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unready engine reported healthy")
	}
	s.stt = &fakeSTT{ready: true}
	resp, _ = http.Get(srv.URL + "/healthz")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready engine reported unhealthy")
	}
}

func TestJobRequiresSource(t *testing.T) {
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", strings.NewReader(`{"language":"ru"}`))
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("sourceless job accepted: %d", resp.StatusCode)
	}
}

func TestJobIDValidation(t *testing.T) {
	for _, bad := range []string{"", "..", "a/b", "a\\b", strings.Repeat("x", 65), "UPPER"} {
		if validJobID(bad) {
			t.Fatalf("invalid id accepted: %q", bad)
		}
	}
	if !validJobID("job-123-abc") {
		t.Fatal("valid id rejected")
	}
}

func TestMiscCoverageBranches(t *testing.T) {
	dir := t.TempDir()
	j := &Job{ID: "job-x", Text: "готово", Segments: []Segment{{Start: -1, End: 1.2, Text: "seg", Speaker: "speaker-0"}}}
	if err := writeJobResult(dir, j); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"result.txt", "result.json", "result.srt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s", name)
		}
	}
	if srtTime(-5) != "00:00:00,000" {
		t.Fatal("negative srt time")
	}
	// saveBounded: over-limit body and pre-existing destination both fail.
	big := filepath.Join(dir, "big.bin")
	if err := saveBounded(big, strings.NewReader("0123456789"), 4); err == nil {
		t.Fatal("oversized body saved")
	}
	if err := saveBounded(filepath.Join(dir, "result.txt"), strings.NewReader("x"), 10); err == nil {
		t.Fatal("existing destination overwritten")
	}
	// Serve refuses a bad engine instead of starting half-wired.
	cfg := testConfig(t)
	cfg.Engine = "nope"
	if _, err := Serve(cfg); err == nil {
		t.Fatal("Serve accepted an unknown engine")
	}
	// jobsRoot: malformed JSON body is a 400, not a panic.
	s := newTestService(t, testConfig(t))
	s.stt = &fakeSTT{ready: true}
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", strings.NewReader("{bad"))
	authReq(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad json: %d", resp.StatusCode)
	}
	// jobsRoot: no source is a 400.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/v1/jobs", strings.NewReader("{}"))
	authReq(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty job: %d", resp.StatusCode)
	}
}

func TestWavAndExtEdges(t *testing.T) {
	good := filepath.Join(t.TempDir(), "ok.wav")
	if err := os.WriteFile(good, writeTestWav(t, 2.0), 0600); err != nil {
		t.Fatal(err)
	}
	if d, err := wavDuration(good); err != nil || d < 1.9 || d > 2.1 {
		t.Fatalf("wavDuration=%v %v", d, err)
	}
	if _, err := wavDuration(filepath.Join(t.TempDir(), "missing.wav")); err == nil {
		t.Fatal("missing file parsed")
	}
	bad := filepath.Join(t.TempDir(), "bad.wav")
	if err := os.WriteFile(bad, []byte("not a wav"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := wavDuration(bad); err == nil {
		t.Fatal("garbage parsed as wav")
	}
	truncated := writeTestWav(t, 0.5)
	truncated[3] = 'X' // still RIFF? no — corrupt the magic instead
	copy(truncated[0:4], "NOPE")
	if err := os.WriteFile(bad, truncated, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := wavDuration(bad); err == nil {
		t.Fatal("bad magic parsed as wav")
	}
	u, _ := url.Parse("https://x.com/file")
	u.Path = ""
	if ext := extensionFromResponse(nil, u); ext != ".bin" {
		t.Fatalf("ext=%q", ext)
	}
	u.Path = "/a/b/meeting.mp3"
	if ext := extensionFromResponse(nil, u); ext != ".mp3" {
		t.Fatalf("ext=%q", ext)
	}
}
