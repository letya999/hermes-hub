package media

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Image sniffing/bounds helpers accept only real png/jpeg/webp bodies and
// reject malformed or oversized content before any decode work.
func TestImageSniffAndBounds(t *testing.T) {
	pngBody := tinyPNG(t)
	if kind, err := sniffImage(pngBody); err != nil || kind != "png" {
		t.Fatalf("png sniff: %q %v", kind, err)
	}
	if kind, err := sniffImage([]byte{0xff, 0xd8, 0xff, 0xe0}); err != nil || kind != "jpeg" {
		t.Fatalf("jpeg sniff: %q %v", kind, err)
	}
	webp := append([]byte("RIFF"), []byte{0, 0, 0, 0}...)
	webp = append(webp, []byte("WEBP")...)
	if kind, err := sniffImage(webp); err != nil || kind != "webp" {
		t.Fatalf("webp sniff: %q %v", kind, err)
	}
	if _, err := sniffImage([]byte("not an image")); err == nil {
		t.Fatal("garbage sniffed")
	}
	w, h, err := imageBounds(pngBody)
	if err != nil || w != 4 || h != 4 {
		t.Fatalf("bounds: %dx%d %v", w, h, err)
	}
	if _, _, err := imageBounds([]byte("junk")); err == nil {
		t.Fatal("bad bounds accepted")
	}
	if _, err := decodeImage([]byte("junk")); err == nil {
		t.Fatal("garbage decoded")
	}
	if _, err := decodeImage(pngBody); err != nil {
		t.Fatalf("png decode: %v", err)
	}
	if imageTarget(".JPEG") != "jpeg" || imageTarget("webp") != "webp" || imageTarget("gif") != "" {
		t.Fatal("imageTarget wrong")
	}
	if imageExt("jpeg") != "jpg" || imageExt("webp") != "webp" || imageExt("png") != "png" {
		t.Fatal("imageExt wrong")
	}
}

// The provider-URL gate allows the configured fal host, requires https
// elsewhere, and rejects credentials, blocked hosts and non-public targets
// without touching the network.
func TestAllowImageURLAndDial(t *testing.T) {
	if err := allowImageURL("https://fal.run/media/x.png", "https://fal.run"); err != nil {
		t.Fatalf("fal host rejected: %v", err)
	}
	for _, raw := range []string{
		"://bad", "https://user@evil.example/x", "http://public.example/x",
		"https://localhost/x", "https://foo.local/x", "https://metadata.google.internal/x",
	} {
		if err := allowImageURL(raw, "https://fal.run"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if err := allowImageURL("https://fal.run/x", ""); err == nil {
		t.Fatal("empty base accepted")
	}
	for _, host := range []string{"localhost", "x.localhost", "foo.local", "bar.internal", "metadata.google.internal"} {
		if !blockedHost(host) {
			t.Fatalf("%q not blocked", host)
		}
	}
	if blockedHost("example.com") {
		t.Fatal("public host blocked")
	}
	for _, ip := range []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.1.2.3"), net.ParseIP("100.64.0.1"), net.ParseIP("0.0.0.0")} {
		if publicIP(ip) {
			t.Fatalf("%v considered public", ip)
		}
	}
	if !publicIP(net.ParseIP("93.184.216.34")) {
		t.Fatal("public ip rejected")
	}
	ctx := context.Background()
	for _, addr := range []string{"no-port", "localhost:80", "127.0.0.1:80", "bad.invalid:443"} {
		if _, err := dialImage(ctx, "tcp", addr, ""); err == nil {
			t.Fatalf("dialed %q", addr)
		}
	}
	// The provider endpoint itself is dialed directly, even on loopback.
	if _, err := dialImage(ctx, "tcp", "127.0.0.1:1", "127.0.0.1:1"); err == nil {
		t.Fatal("expected refused dial, got nil error")
	}
}

// ConvertImage decodes the workspace source, re-encodes the target format
// under artifacts/images, and reports bounds; getLimited fetches only the
// configured provider host and enforces the byte cap.
func TestConvertImageAndGetLimited(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in.png"), tinyPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	s := New(root)

	out, err := s.ConvertImage(context.Background(), "thumb", "in.png", "jpeg")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if out["format"] != "jpeg" || out["width"] != 4 || out["path"] != "artifacts/images/thumb.jpg" {
		t.Fatalf("convert result: %+v", out)
	}
	if _, err := s.ConvertImage(context.Background(), "thumb", "in.png", "png"); err == nil {
		t.Fatal("same-format convert accepted")
	}
	if _, err := s.ConvertImage(context.Background(), "bad name!", "in.png", "jpeg"); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := s.ConvertImage(context.Background(), "x", "in.png", "gif"); err == nil {
		t.Fatal("unsupported format accepted")
	}
	if _, err := s.ConvertImage(context.Background(), "x", "missing.png", "jpeg"); err == nil {
		t.Fatal("missing source converted")
	}

	fetcher := &Session{HTTP: &http.Client{}}
	if _, err := fetcher.getLimited(context.Background(), "http://x", "http://x", 8); err == nil {
		t.Fatal("request without real host accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("a"), 32))
	}))
	defer srv.Close()
	body, err := fetcher.getLimited(context.Background(), srv.URL+"/img", srv.URL, 1024)
	if err != nil || len(body) != 32 {
		t.Fatalf("getLimited: %d %v", len(body), err)
	}
	if _, err := fetcher.getLimited(context.Background(), srv.URL+"/img", srv.URL, 8); err == nil {
		t.Fatal("oversized body accepted")
	}
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	if _, err := fetcher.getLimited(context.Background(), notFound.URL+"/img", notFound.URL, 1024); err == nil {
		t.Fatal("HTTP 404 body accepted")
	}
	noHTTP := &Session{}
	if _, err := noHTTP.getLimited(context.Background(), srv.URL, srv.URL, 8); err == nil {
		t.Fatal("nil client accepted")
	}
}
