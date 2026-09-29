package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openSession(t *testing.T) (*Session, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return New(root), dir
}

func writeWorkspace(t *testing.T, dir, rel string, body []byte) {
	t.Helper()
	target := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func grantFile(t *testing.T, dir, baseURL string, granted bool) string {
	t.Helper()
	body := "model:\n  default: fixture-model\n  base_url: " + baseURL + "/v1\n"
	if granted {
		body += "image_gen:\n  provider: fal\n  model: " + ImageModel + "\n"
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_HERMES_CONFIG", path)
	return path
}

func TestImageGenCapabilityRejectsUnknownChoices(t *testing.T) {
	if _, err := (ImageGen{Provider: "guessed"}).Normalize(); err == nil {
		t.Fatal("unknown provider")
	}
	if _, err := (ImageGen{Provider: ProviderCLIProxy, Model: "gpt-chat"}).Normalize(); err == nil {
		t.Fatal("unknown model")
	}
	if _, err := (ImageGen{Model: "gemini-3.7-flash-high"}).Normalize(); err == nil {
		t.Fatal("chat model accepted as an image model")
	}
	if _, err := (ImageGen{Model: "gemini-3.1-flash-image"}).Normalize(); err != nil {
		t.Fatal(err)
	}
	if _, err := (ImageGen{Delivery: "both"}).Normalize(); err == nil {
		t.Fatal("unknown delivery")
	}
	got, err := (ImageGen{}).Normalize()
	if err != nil || got.Provider != ProviderCLIProxy || got.Model != CLIProxyDefaultModel || got.Delivery != DeliveryWorkspace {
		t.Fatal(got, err)
	}
	kept, err := (ImageGen{Provider: FalProvider, Model: ModelFromChat, Delivery: DeliveryURL}).Normalize()
	if err != nil || kept.Model != ModelFromChat || kept.Delivery != DeliveryURL {
		t.Fatal(kept, err)
	}
	if _, err := kept.Resolve("fixture-model"); err == nil || !strings.Contains(err.Error(), "chat model") {
		t.Fatal(err)
	}
	id, err := (ImageGen{Model: ModelFromChat}).Resolve(CLIProxyDefaultModel)
	if err != nil || id != CLIProxyDefaultModel {
		t.Fatal(id, err)
	}
	if _, err := (ImageGen{Model: ModelFromChat}).Resolve("gemini-3.7-flash-high"); err == nil {
		t.Fatal("resolved a chat model")
	}
	chatImage, err := (ImageGen{Model: ModelFromChat}).Resolve("gemini-3.1-flash-image")
	if err != nil || chatImage != "gemini-3.1-flash-image" {
		t.Fatal(chatImage, err)
	}
}

func TestMountedImageGenRejectsUnknownFields(t *testing.T) {
	s, dir := openSession(t)
	path := filepath.Join(dir, "config.yaml")
	body := "model:\n  default: m\n  base_url: http://127.0.0.1:9/v1\nimage_gen:\n  provider: cliproxy\n  steps: 9\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_HERMES_CONFIG", path)
	t.Setenv("OPENAI_API_KEY", "openai-secret-value")
	if s.ImageGranted() {
		t.Fatal("unknown image_gen field granted")
	}
	if _, err := s.Generate(context.Background(), "pic", "cat"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatal(err)
	}
}

func TestURLDeliveryDoesNotInventAURL(t *testing.T) {
	pngBody := pngBytes(t, 1, 1)
	encoded := base64.StdEncoding.EncodeToString(pngBody)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + encoded + `"}]}`))
	}))
	defer server.Close()
	s, dir := openSession(t)
	t.Setenv("OPENAI_API_KEY", "openai-secret-value")
	cfg := "model:\n  default: " + CLIProxyDefaultModel + "\n  base_url: " + server.URL + "/v1\nimage_gen:\n  provider: cliproxy\n  delivery: url\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_HERMES_CONFIG", path)
	if _, err := s.Generate(context.Background(), "pic", "a square"); err == nil || !strings.Contains(err.Error(), "did not return a URL") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts", "images", "pic.png")); !os.IsNotExist(err) {
		t.Fatal("url delivery wrote a file", err)
	}
}

func TestDocumentEditAndConvertStayInTheWorkspace(t *testing.T) {
	s, dir := openSession(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, "note", "txt", "hello"); err != nil {
		t.Fatal(err)
	}
	edited, err := s.EditDocument(ctx, "artifacts/documents/note.txt", "changed")
	if err != nil || edited["path"] != "artifacts/documents/note.txt" {
		t.Fatal(edited, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "artifacts", "documents", "note.txt"))
	if err != nil || string(body) != "changed" {
		t.Fatal(err, string(body))
	}
	if _, err := s.EditDocument(ctx, "note.txt", "nope"); err == nil {
		t.Fatal("edited a source file")
	}
	converted, err := s.ConvertDocument(ctx, "artifacts/documents/note.txt", "note", "html")
	if err != nil || converted["path"] != "artifacts/documents/note.html" {
		t.Fatal(converted, err)
	}
	page, err := os.ReadFile(filepath.Join(dir, "artifacts", "documents", "note.html"))
	if err != nil || !strings.Contains(string(page), "changed") || strings.Contains(string(page), "<script>") {
		t.Fatal(err, string(page))
	}
	if _, err := s.ConvertDocument(ctx, "artifacts/documents/note.txt", "sheet", "csv"); err == nil {
		t.Fatal("converted prose to csv")
	}
	if _, err := s.ConvertDocument(ctx, "artifacts/documents/note.txt", "note", "txt"); err == nil {
		t.Fatal("converted to the same format")
	}
}

func TestImageConvertIsLocal(t *testing.T) {
	s, dir := openSession(t)
	writeWorkspace(t, dir, "dot.png", pngBytes(t, 2, 2))
	out, err := s.ConvertImage(context.Background(), "dot", "dot.png", "jpeg")
	if err != nil || out["path"] != "artifacts/images/dot.jpg" || out["format"] != "jpeg" {
		t.Fatal(out, err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "artifacts", "images", "dot.jpg"))
	if err != nil || len(stored) < 3 || stored[0] != 0xff {
		t.Fatal(err)
	}
	if _, err := s.ConvertImage(context.Background(), "again", "dot.png", "png"); err == nil {
		t.Fatal("unchanged format")
	}
	web, err := s.ConvertImage(context.Background(), "web", "dot.png", "webp")
	if err != nil || web["path"] != "artifacts/images/web.webp" || web["format"] != "webp" {
		t.Fatal(web, err)
	}
	back, err := s.ConvertImage(context.Background(), "back", "artifacts/images/web.webp", "png")
	if err != nil || back["path"] != "artifacts/images/back.png" || back["format"] != "png" {
		t.Fatal(back, err)
	}
	if _, err := s.ConvertImage(context.Background(), "gif", "dot.png", "gif"); err == nil {
		t.Fatal("gif accepted")
	}
}

func TestCLIProxyGenerateEditAndURLDelivery(t *testing.T) {
	pngBody := pngBytes(t, 2, 2)
	encoded := base64.StdEncoding.EncodeToString(pngBody)
	var sawEdit atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer openai-secret-value" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/images/generations":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("payload: %v", err)
			}
			if len(payload) != 5 || payload["model"] != CLIProxyDefaultModel || payload["n"] != float64(1) || payload["size"] != CLIProxyImageSize || payload["response_format"] != "b64_json" {
				t.Errorf("payload %#v", payload)
			}
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + encoded + `"}]}`))
		case "/v1/images/edits":
			sawEdit.Store(true)
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			if r.FormValue("model") != "grok-imagine-image" || r.FormValue("prompt") != "make it blue" || r.FormValue("response_format") != "url" {
				t.Errorf("form model=%q prompt=%q format=%q", r.FormValue("model"), r.FormValue("prompt"), r.FormValue("response_format"))
			}
			if _, _, err := r.FormFile("image"); err != nil {
				t.Errorf("image file: %v", err)
			}
			_, _ = w.Write([]byte(`{"data":[{"url":"` + server.URL + `/out.png"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s, dir := openSession(t)
	t.Setenv("OPENAI_API_KEY", "openai-secret-value")
	cfg := filepath.Join(dir, "config.yaml")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HUB_HERMES_CONFIG", cfg)
	}
	base := "model:\n  default: " + CLIProxyDefaultModel + "\n  base_url: " + server.URL + "/v1\n"
	write(base + "image_gen:\n  provider: cliproxy\n  delivery: workspace\n")
	made, err := s.Generate(context.Background(), "pic", "a square")
	if err != nil || made["path"] != "artifacts/images/pic.png" || made["model"] != CLIProxyDefaultModel || made["delivery"] != DeliveryWorkspace {
		t.Fatal(made, err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "artifacts", "images", "pic.png"))
	if err != nil || !bytes.Equal(stored, pngBody) {
		t.Fatal(err)
	}
	writeWorkspace(t, dir, "source.png", pngBody)
	write(base + "image_gen:\n  provider: cliproxy\n  model: grok-imagine-image\n  delivery: url\n")
	edited, err := s.Edit(context.Background(), "next", "source.png", "make it blue")
	if err != nil || edited["url"] != server.URL+"/out.png" || edited["delivery"] != DeliveryURL || !sawEdit.Load() {
		t.Fatal(edited, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts", "images", "next.png")); !os.IsNotExist(err) {
		t.Fatal("url delivery wrote a file", err)
	}
	write(base + "image_gen:\n  provider: fal\n  model: " + ImageModel + "\n")
	t.Setenv("FAL_KEY", "fal-secret-value")
	if _, err := s.Edit(context.Background(), "nope", "source.png", "change"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatal(err)
	}
}

func TestCLIProxyChatImageUsesChatCompletions(t *testing.T) {
	jpegBody := jpegBytes(t)
	encoded := base64.StdEncoding.EncodeToString(jpegBody)
	dataURL := "data:image/jpeg;base64," + encoded
	var imagesHits atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/images/generations" || r.URL.Path == "/v1/images/edits" {
			imagesHits.Add(1)
			http.Error(w, "wrong route", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer openai-secret-value" {
			t.Errorf("auth header")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("payload: %v", err)
			return
		}
		if payload["model"] != "gemini-3.1-flash-image" {
			t.Errorf("model %#v", payload["model"])
		}
		if _, ok := payload["size"]; ok || payload["response_format"] != nil {
			t.Errorf("images-endpoint field in chat body")
		}
		mods, _ := payload["modalities"].([]any)
		if len(mods) != 2 || mods[0] != "image" || mods[1] != "text" {
			t.Errorf("modalities %#v", payload["modalities"])
		}
		cfg, _ := payload["image_config"].(map[string]any)
		if len(cfg) != 1 || cfg["aspect_ratio"] != CLIProxyChatAspect {
			t.Errorf("image_config %#v", payload["image_config"])
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 1 {
			t.Errorf("messages %#v", payload["messages"])
			return
		}
		msg, _ := messages[0].(map[string]any)
		wantRemote := false
		switch content := msg["content"].(type) {
		case string:
			if content == "" {
				t.Error("empty prompt")
			}
			wantRemote = content == "remote square"
		case []any:
			if len(content) != 2 {
				t.Errorf("edit content %#v", content)
			}
			part, _ := content[1].(map[string]any)
			imageURL, _ := part["image_url"].(map[string]any)
			ref, _ := imageURL["url"].(string)
			if !strings.HasPrefix(ref, "data:image/png;base64,") {
				t.Errorf("edit image %q", ref[:min(24, len(ref))])
			}
		default:
			t.Errorf("content %T", msg["content"])
		}
		imageURL := dataURL
		if wantRemote {
			imageURL = server.URL + "/out.jpg"
		}
		raw, err := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"images": []any{map[string]any{"image_url": map[string]string{"url": imageURL}}},
				},
			}},
		})
		if err != nil {
			t.Errorf("response: %v", err)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	s, dir := openSession(t)
	t.Setenv("OPENAI_API_KEY", "openai-secret-value")
	cfg := filepath.Join(dir, "config.yaml")
	write := func(model, delivery, chatDefault string) {
		t.Helper()
		body := "model:\n  default: " + chatDefault + "\n  base_url: " + server.URL + "/v1\nimage_gen:\n  provider: cliproxy\n  model: " + model + "\n  delivery: " + delivery + "\n"
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HUB_HERMES_CONFIG", cfg)
	}
	write("gemini-3.1-flash-image", DeliveryWorkspace, "gemini-3.7-flash-high")
	made, err := s.Generate(context.Background(), "gem", "a flat red square")
	if err != nil || made["path"] != "artifacts/images/gem.jpg" || made["format"] != "jpeg" || made["model"] != "gemini-3.1-flash-image" {
		t.Fatal(made, err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "artifacts", "images", "gem.jpg"))
	if err != nil || !bytes.Equal(stored, jpegBody) {
		t.Fatal(err)
	}
	writeWorkspace(t, dir, "source.png", pngBytes(t, 2, 2))
	edited, err := s.Edit(context.Background(), "gemed", "source.png", "make the square blue")
	if err != nil || edited["path"] != "artifacts/images/gemed.jpg" || edited["format"] != "jpeg" {
		t.Fatal(edited, err)
	}
	write("gemini-3.1-flash-image", DeliveryURL, "gemini-3.7-flash-high")
	if _, err := s.Generate(context.Background(), "nourl", "a square"); err == nil || !strings.Contains(err.Error(), "did not return a URL") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifacts", "images", "nourl.jpg")); !os.IsNotExist(err) {
		t.Fatal("data url wrote a file", err)
	}
	write("chat", DeliveryURL, "gemini-3.1-flash-image")
	linked, err := s.Generate(context.Background(), "link", "remote square")
	if err != nil || linked["url"] != server.URL+"/out.jpg" || linked["model"] != "gemini-3.1-flash-image" {
		t.Fatal(linked, err)
	}
	write("gemini-3.7-flash-high", DeliveryWorkspace, "gemini-3.7-flash-high")
	if _, err := s.Generate(context.Background(), "nope", "a square"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatal(err)
	}
	if imagesHits.Load() != 0 {
		t.Fatal("images endpoint was called")
	}
}

func TestHTMLExtractDropsActiveContent(t *testing.T) {
	got := htmlText(`<!-- <p>hidden</p> --> <script>secret()</script><style>x</style><p>Hello <b>there</b></p>`)
	if got != "Hello there" {
		t.Fatalf("html text %q", got)
	}
	if strings.Contains(htmlText(`<script>nope`), "nope") {
		t.Fatal("unclosed script leaked")
	}
}

func TestDocumentsAreSeparateEffects(t *testing.T) {
	s, dir := openSession(t)
	ctx := context.Background()
	created, err := s.Create(ctx, "note", "html", "Hello <script>nope</script>")
	if err != nil {
		t.Fatal(err)
	}
	if created["path"] != "artifacts/documents/note.html" {
		t.Fatal(created)
	}
	if _, err := s.Create(ctx, "note", "html", "again"); err == nil {
		t.Fatal("create overwrote an artifact")
	}
	body, err := os.ReadFile(filepath.Join(dir, "artifacts", "documents", "note.html"))
	if err != nil || strings.Contains(string(body), "<script>") {
		t.Fatalf("create stored active markup: %v %s", err, body)
	}
	out, err := s.Extract(ctx, created["path"].(string))
	if err != nil || out["text"] != "Hello <script>nope</script>" || out["format"] != "html" || out["pages"].(int) != 1 {
		t.Fatal(out, err)
	}
	writeWorkspace(t, dir, "sheet.csv", []byte("a,b\n1,2\n"))
	csv, err := s.Extract(ctx, "sheet.csv")
	if err != nil || csv["format"] != "csv" || !strings.Contains(csv["text"].(string), "a,b") {
		t.Fatal(csv, err)
	}
	writeWorkspace(t, dir, "brief.pdf", []byte("%PDF-1.7"))
	if _, err := s.Extract(ctx, "brief.pdf"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatal(err)
	}
	for _, rel := range []string{"../note.md", "/etc/passwd", `a\b.md`, "C:/Windows/note.md"} {
		if _, err := s.Extract(ctx, rel); err == nil {
			t.Fatal("accepted", rel)
		}
	}
	if _, err := s.Create(ctx, "../note", "md", "x"); err == nil {
		t.Fatal("escaped create name")
	}
	if _, err := s.Create(ctx, "wide", "docx", "x"); err == nil {
		t.Fatal("docx create")
	}
	writeWorkspace(t, dir, "page.htm", []byte("<p>from htm</p>"))
	htm, err := s.Extract(ctx, "page.htm")
	if err != nil || htm["format"] != "html" || htm["text"] != "from htm" {
		t.Fatal(htm, err)
	}
}

func TestArtifactsSurviveReopenAndRemoveExplicitly(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(root)
	if _, err := s.Create(context.Background(), "kept", "md", "still here"); err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
	again, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	s = New(again)
	out, err := s.Extract(context.Background(), "artifacts/documents/kept.md")
	if err != nil || out["text"] != "still here" {
		t.Fatal(out, err)
	}
	if _, err := s.Remove("drafts/kept.md"); err == nil {
		t.Fatal("removed a non-artifact")
	}
	if _, err := s.Remove("../artifacts/documents/kept.md"); err == nil {
		t.Fatal("remove escaped")
	}
	removed, err := s.Remove("artifacts/documents/kept.md")
	if err != nil || removed["removed"] != true {
		t.Fatal(removed, err)
	}
	if _, err := s.Extract(context.Background(), "artifacts/documents/kept.md"); err == nil {
		t.Fatal("removed artifact still readable")
	}
}

func TestCrossWorkspaceAccessFailsClosed(t *testing.T) {
	alice, aliceDir := openSession(t)
	bob, _ := openSession(t)
	if _, err := alice.Create(context.Background(), "private", "txt", "alice only"); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Extract(context.Background(), "artifacts/documents/private.txt"); err == nil {
		t.Fatal("bob read alice")
	}
	if _, err := bob.Extract(context.Background(), filepath.Join(aliceDir, "artifacts", "documents", "private.txt")); err == nil {
		t.Fatal("absolute path accepted")
	}
}

func TestSecretsStayOutOfResultsAndArtifacts(t *testing.T) {
	s, dir := openSession(t)
	t.Setenv("FAL_KEY", "fal-secret-value")
	t.Setenv("OPENAI_API_KEY", "openai-secret-value")
	writeWorkspace(t, dir, "leak.md", []byte("token fal-secret-value stays on disk"))
	out, err := s.Extract(context.Background(), "leak.md")
	if err != nil || strings.Contains(out["text"].(string), "fal-secret-value") || !strings.Contains(out["text"].(string), "[redacted]") {
		t.Fatal(out, err)
	}
	disk, err := os.ReadFile(filepath.Join(dir, "leak.md"))
	if err != nil || !strings.Contains(string(disk), "fal-secret-value") {
		t.Fatal("extract rewrote the source file")
	}
	if _, err := s.Create(context.Background(), "nope", "txt", "openai-secret-value"); err == nil || strings.Contains(err.Error(), "openai-secret-value") {
		t.Fatal(err)
	}
}

func TestPageAndByteBounds(t *testing.T) {
	s, dir := openSession(t)
	writeWorkspace(t, dir, "big.md", bytes.Repeat([]byte("a"), MaxPages*RunesPerPage+1))
	if _, err := s.Extract(context.Background(), "big.md"); err == nil {
		t.Fatal("page limit")
	}
	if _, err := s.Create(context.Background(), "big", "txt", strings.Repeat("b", MaxDocumentBytes+1)); err == nil {
		t.Fatal("byte limit")
	}
}

func TestGateFailsClosedWhenFull(t *testing.T) {
	var g gate
	g.max = 1
	if err := g.enter(); err != nil {
		t.Fatal(err)
	}
	if err := g.enter(); err == nil {
		t.Fatal("second enter")
	}
	g.leave()
	if err := g.enter(); err != nil {
		t.Fatal(err)
	}
}

func TestImageInspectAndGenerateAreSeparateContracts(t *testing.T) {
	pngBody := pngBytes(t, 2, 3)
	var sawVision, sawFal atomic.Bool
	var imageURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			sawVision.Store(true)
			if r.Header.Get("Authorization") != "Bearer openai-secret-value" || strings.Contains(r.Header.Get("Authorization"), "fal-secret-value") {
				t.Errorf("vision auth %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a red pixel"}}]}`))
		case "/fal-ai/flux-2/klein/9b":
			sawFal.Store(true)
			if r.Header.Get("Authorization") != "Key fal-secret-value" || strings.Contains(r.Header.Get("Authorization"), "openai-secret-value") {
				t.Errorf("generation auth %q", r.Header.Get("Authorization"))
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("payload: %v", err)
			}
			if _, extra := payload["num_images"]; extra || len(payload) != 5 || payload["image_size"] != FalImageSize || payload["output_format"] != FalFormat || payload["num_inference_steps"] != float64(FalSteps) || payload["enable_safety_checker"] != false {
				t.Errorf("payload %#v", payload)
			}
			_, _ = w.Write([]byte(`{"images":[{"url":"` + imageURL + `"}]}`))
		case "/img.png":
			_, _ = w.Write(pngBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	imageURL = server.URL + "/img.png"

	s, dir := openSession(t)
	s.FalBase = server.URL
	t.Setenv("OPENAI_API_KEY", "openai-secret-value")
	t.Setenv("FAL_KEY", "fal-secret-value")
	grantFile(t, dir, server.URL, true)
	writeWorkspace(t, dir, "dot.png", pngBody)

	seen, err := s.Inspect(context.Background(), "dot.png")
	if err != nil || seen["text"] != "a red pixel" || seen["width"] != 2 || seen["height"] != 3 || seen["secret_values_included"] != false {
		t.Fatal(seen, err)
	}
	if !sawVision.Load() {
		t.Fatal("vision endpoint was not called")
	}
	writeWorkspace(t, dir, "photo.jpg", jpegBytes(t))
	jpg, err := s.Inspect(context.Background(), "photo.jpg")
	if err != nil || jpg["format"] != "jpeg" || jpg["width"] != 2 || jpg["height"] != 2 {
		t.Fatal(jpg, err)
	}
	made, err := s.Generate(context.Background(), "pic", "a red square")
	if err != nil || made["path"] != "artifacts/images/pic.png" || made["model"] != ImageModel {
		t.Fatal(made, err)
	}
	if !sawFal.Load() {
		t.Fatal("generation endpoint was not called")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "artifacts", "images", "pic.png"))
	if err != nil || bytes.Contains(stored, []byte("fal-secret-value")) || bytes.Contains(stored, []byte("openai-secret-value")) {
		t.Fatal(err)
	}
	if _, err := s.Inspect(context.Background(), "dot.gif"); err == nil {
		t.Fatal("gif accepted")
	}
	writeWorkspace(t, dir, "wide.png", pngBytes(t, MaxEdge+1, 1))
	if _, err := s.Inspect(context.Background(), "wide.png"); err == nil {
		t.Fatal("pixel limit")
	}
	if _, err := s.Generate(context.Background(), "leak", "fal-secret-value in the prompt"); err == nil || strings.Contains(err.Error(), "fal-secret-value") {
		t.Fatal(err)
	}
}

func TestStaleGrantAndPrivateImageURLFailClosed(t *testing.T) {
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/img.png" {
			fetches.Add(1)
		}
		if r.URL.Path != "/fal-ai/flux-2/klein/9b" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"images":[{"url":"https://10.1.2.3/secret.png"}]}`))
	}))
	defer server.Close()
	s, dir := openSession(t)
	s.FalBase = server.URL
	t.Setenv("FAL_KEY", "fal-secret-value")
	t.Setenv("OPENAI_API_KEY", "openai-secret-value")
	path := grantFile(t, dir, "http://127.0.0.1:9", false)
	if s.ImageGranted() {
		t.Fatal("grant without image_gen")
	}
	if _, err := s.Generate(context.Background(), "pic", "cat"); err == nil || strings.Contains(err.Error(), "fal-secret-value") {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model:\n  default: m\n  base_url: http://127.0.0.1:9/v1\nimage_gen:\n  provider: fal\n  model: "+ImageModel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Generate(context.Background(), "pic", "cat"); err == nil || fetches.Load() != 0 {
		t.Fatalf("private image fetched: %v fetches=%d", err, fetches.Load())
	}
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("bad fal-secret-value"))
	}))
	defer echo.Close()
	s.FalBase = echo.URL
	if _, err := s.Generate(context.Background(), "pic", "cat"); err == nil || strings.Contains(err.Error(), "fal-secret-value") {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model:\n  default: m\n  base_url: http://127.0.0.1:9/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Generate(context.Background(), "pic", "cat"); err == nil {
		t.Fatal("stale grant still generated")
	}
}

func TestGenerateTimeoutReleasesTheCall(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
		case <-timer.C:
			w.WriteHeader(http.StatusGatewayTimeout)
		}
	}))
	defer blocked.Close()
	s, dir := openSession(t)
	s.FalBase = blocked.URL
	t.Setenv("FAL_KEY", "fal-secret-value")
	grantFile(t, dir, "http://127.0.0.1:9", true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.Generate(ctx, "pic", "cat"); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatal(err)
	}
	var fastURL string
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/img.png" {
			_, _ = w.Write(pngBytes(t, 1, 1))
			return
		}
		_, _ = w.Write([]byte(`{"images":[{"url":"` + fastURL + `/img.png"}]}`))
	}))
	defer fast.Close()
	fastURL = fast.URL
	s.FalBase = fast.URL
	if _, err := s.Generate(context.Background(), "next", "cat"); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateConcurrencyIsBounded(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	s, dir := openSession(t)
	s.FalBase = server.URL
	s.gen.max = 1
	t.Setenv("FAL_KEY", "fal-secret-value")
	grantFile(t, dir, "http://127.0.0.1:9", true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.Generate(context.Background(), "one", "cat")
	}()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("generation did not start")
	}
	limited, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if _, err := s.Generate(limited, "two", "cat"); err == nil || !strings.Contains(err.Error(), "concurrency") {
		t.Fatal(err)
	}
	close(release)
	<-done
}

func TestGenerateDoesNotFollowRedirect(t *testing.T) {
	var fetched atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/img.png" {
			fetched.Add(1)
			return
		}
		http.Redirect(w, r, "/img.png", http.StatusFound)
	}))
	defer server.Close()
	s, dir := openSession(t)
	s.FalBase = server.URL
	t.Setenv("FAL_KEY", "fal-secret-value")
	grantFile(t, dir, "http://127.0.0.1:9", true)
	if _, err := s.Generate(context.Background(), "pic", "cat"); err == nil || fetched.Load() != 0 {
		t.Fatalf("redirect followed: %v fetches=%d", err, fetched.Load())
	}
}

func TestFalResponseMustBeOneImage(t *testing.T) {
	for _, body := range []string{
		`{"images":[]}`,
		`{"images":[{"url":"https://203.0.113.5/a.png"},{"url":"https://203.0.113.5/b.png"}]}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		s, dir := openSession(t)
		s.FalBase = server.URL
		t.Setenv("FAL_KEY", "fal-secret-value")
		grantFile(t, dir, "http://127.0.0.1:9", true)
		_, err := s.Generate(context.Background(), "pic", "cat")
		server.Close()
		if err == nil || strings.Contains(err.Error(), "203.0.113.5") {
			t.Fatal(err)
		}
	}
}

func TestImageDownloadDialRejectsPrivate(t *testing.T) {
	for _, addr := range []string{"10.1.2.3:443", "100.64.1.1:443", "127.0.0.1:80", "169.254.169.254:80", "metadata.google.internal:80", "localhost:80", "[::1]:80"} {
		conn, err := dialImage(context.Background(), "tcp", addr, "fal.run")
		if err == nil {
			_ = conn.Close()
			t.Fatal("dialed", addr)
		}
	}
}
