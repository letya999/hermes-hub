package communication

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLogs swaps the package logger's output for a test buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return buf
}

// Normal logs carry IDs, lengths and status — never message bodies, file
// names or sensitive URLs, even when they contain credential-shaped text.
func TestMessageLogsCarryNoContentByDefault(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	buf := captureLogs(t)
	ctx := context.Background()
	user := g.user("alice")
	if _, err := g.spool.ResolveTask(user, 11, 0, true); err != nil {
		t.Fatal(err)
	}
	secret := "OPENAI_API_KEY=sk-proj-abcdef1234567890secret"
	sensitive := "https://internal.example.com/files/report.pdf?sig=zzz"
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "проверь "+secret+" и "+sensitive, 11, 0)); err != nil {
		t.Fatal(err)
	}
	if err := g.queueDelivery(ctx, "d1", 11, 0, "ответ с "+sensitive); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	out := buf.String()
	for _, leaked := range []string{secret, "sk-proj", sensitive, "report.pdf", "internal.example.com"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("normal log leaked %q:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "text_len=") || !strings.Contains(out, "update_id=1") {
		t.Fatalf("normal log lost its IDs/lengths:\n%s", out)
	}
}

// Content capture is explicit and time-bounded: inside the window text is
// logged but still secret-redacted; after expiry logs revert to lengths.
func TestContentCaptureWindowIsBoundedAndRedacted(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	buf := captureLogs(t)
	ctx := context.Background()
	user := g.user("alice")
	if _, err := g.spool.ResolveTask(user, 11, 0, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	g.now = func() time.Time { return now }
	g.contentUntil = now.Add(10 * time.Minute)
	text := "найди токен sk-proj-abcdef1234567890secret в письме"
	if err := g.handleUpdate(ctx, taskUpdate(2, 2, text, 11, 0)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "text=") {
		t.Fatalf("capture window dropped the text field:\n%s", out)
	}
	if strings.Contains(out, "sk-proj-abcdef1234567890secret") {
		t.Fatalf("capture window logged an unredacted secret:\n%s", out)
	}
	if !strings.Contains(out, "найди токен") {
		t.Fatalf("capture window should carry redacted text:\n%s", out)
	}
	// Past the expiry the same message returns to length-only logging.
	g.contentUntil = now.Add(-time.Minute)
	if err := g.handleUpdate(ctx, taskUpdate(3, 3, text, 11, 0)); err != nil {
		t.Fatal(err)
	}
	out = buf.String()
	if !strings.Contains(out, "update_id=3") || strings.Contains(out, `update_id=3 chat_id=11 message_id=3 text="`) {
		t.Fatalf("expired capture still logged text:\n%s", out)
	}
}

// The env knob parses an RFC3339 deadline and clamps far-future values so a
// typo cannot pin content logging open; malformed values fail the render.
func TestContentCaptureEnvBounded(t *testing.T) {
	c := testConfig(t)
	t.Setenv("HUB_LOG_CONTENT_UNTIL", "not-a-time")
	if _, err := New(c); err == nil {
		t.Fatal("malformed HUB_LOG_CONTENT_UNTIL accepted")
	}
	t.Setenv("HUB_LOG_CONTENT_UNTIL", time.Now().Add(30*24*time.Hour).Format(time.RFC3339))
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if g.contentUntil.After(time.Now().Add(time.Hour + time.Minute)) {
		t.Fatalf("capture window not clamped: %s", g.contentUntil)
	}
}
