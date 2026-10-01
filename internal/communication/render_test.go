package communication

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/letya999/hermes-hub/internal/identity"
)

func tgUTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func TestRenderTelegramFormatsMarkdown(t *testing.T) {
	src := "### Итог\n\nТекст с **жирным**, *курсивом*, ~~strike~~ и `код`.\n\n- один\n- два\n\n1. первый\n2. второй\n\n> цитата\n\n```go\nfmt.Println(\"x\")\n```\n\n[ссылка](https://example.com?a=1&b=2)\n\n---\n"
	parts, degraded := renderTelegram(src)
	if degraded || len(parts) != 1 {
		t.Fatalf("parts=%d degraded=%v", len(parts), degraded)
	}
	got := parts[0]
	for _, want := range []string{
		"<b>Итог</b>",
		"<b>жирным</b>", "<i>курсивом</i>", "<s>strike</s>", "<code>код</code>",
		"• один", "• два", "1. первый", "2. второй",
		"<blockquote>цитата</blockquote>",
		`<pre><code class="language-go">`, "fmt.Println(&#34;x&#34;)", "</code></pre>",
		`<a href="https://example.com?a=1&amp;b=2">ссылка</a>`,
		"———",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestRenderTelegramEscapesHostileHTML(t *testing.T) {
	src := "Текст с <script>alert(1)</script> и **a <img src=x> b**.\n\n<div class=\"x\">raw block</div>\n\n[boom](javascript:alert(1)) и [ок](https://ok.example)"
	parts, degraded := renderTelegram(src)
	got := strings.Join(parts, "\n")
	if !degraded {
		t.Fatal("raw html block not flagged")
	}
	if strings.Contains(got, "<script>") || strings.Contains(got, "<img src=x>") || strings.Contains(got, "javascript:") || strings.Contains(got, `class="x"`) {
		t.Fatalf("hostile html leaked: %q", got)
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;img src=x&gt;", "&lt;div", "boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if !strings.Contains(got, `<a href="https://ok.example">ок</a>`) {
		t.Fatalf("safe link lost: %q", got)
	}
}

func TestRenderTelegramTableDegradesToGridAndSource(t *testing.T) {
	src := "| Имя | Значение |\n|---|---|\n| альфа | 1 |\n| бета-β | 22 |\n"
	parts, degraded := renderTelegram(src)
	if !degraded || len(parts) != 1 {
		t.Fatalf("parts=%d degraded=%v", len(parts), degraded)
	}
	got := parts[0]
	if !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
		t.Fatalf("table not in pre: %q", got)
	}
	if !strings.Contains(got, "Имя") || !strings.Contains(got, "-+-") || !strings.Contains(got, "бета-β") {
		t.Fatalf("grid broken: %q", got)
	}
}

func TestRenderTelegramSplitsLongAnswerInOrder(t *testing.T) {
	var paragraphs []string
	for i := 0; i < 6; i++ {
		paragraphs = append(paragraphs, "Абзац "+strconv.Itoa(i)+": "+strings.Repeat("текст с кириллицей ", 100))
	}
	src := strings.Join(paragraphs, "\n\n")
	parts, degraded := renderTelegram(src)
	if degraded || len(parts) < 2 {
		t.Fatalf("parts=%d degraded=%v", len(parts), degraded)
	}
	for i, p := range parts {
		if tgUTF16Len(p) > telegramPartLimit {
			t.Fatalf("part %d exceeds limit: %d", i, tgUTF16Len(p))
		}
		if !utf8.ValidString(p) {
			t.Fatalf("part %d has broken utf8", i)
		}
	}
	joined := strings.Join(parts, "")
	for i := 0; i < 6; i++ {
		if !strings.Contains(joined, "Абзац "+strconv.Itoa(i)+":") {
			t.Fatalf("paragraph %d lost", i)
		}
	}
	// Order: each paragraph index must appear after the previous one.
	last := -1
	for i := 0; i < 6; i++ {
		idx := strings.Index(joined, "Абзац "+strconv.Itoa(i)+":")
		if idx <= last {
			t.Fatalf("paragraph %d out of order", i)
		}
		last = idx
	}
}

func TestRenderTelegramNeverSplitsEmoji(t *testing.T) {
	// Emoji are 2 UTF-16 units; a naive rune boundary could split a surrogate pair.
	src := strings.Repeat("😀", 2100)
	parts, _ := renderTelegram(src)
	for i, p := range parts {
		if !utf8.ValidString(p) || tgUTF16Len(p) > telegramPartLimit {
			t.Fatalf("part %d invalid/over limit", i)
		}
	}
	total := 0
	for _, p := range parts {
		total += strings.Count(p, "😀")
	}
	if total != 2100 {
		t.Fatalf("emoji lost: %d", total)
	}
}

func TestRenderTelegramOversizedCodeFenceStaysWrapped(t *testing.T) {
	src := "```go\n" + strings.Repeat("line := fmt.Sprintf(\"%d\", i)\n", 300) + "```"
	parts, _ := renderTelegram(src)
	if len(parts) < 2 {
		t.Fatalf("no split: %d", len(parts))
	}
	for i, p := range parts {
		if !strings.HasPrefix(p, `<pre><code class="language-go">`) || !strings.HasSuffix(p, "</code></pre>") {
			t.Fatalf("part %d not a closed code block", i)
		}
		if tgUTF16Len(p) > telegramPartLimit {
			t.Fatalf("part %d over limit", i)
		}
	}
	if !strings.Contains(parts[0], "line :=") || !strings.Contains(parts[len(parts)-1], "line :=") {
		t.Fatal("code content lost")
	}
}

func TestRenderSlackFormatsMrkdwn(t *testing.T) {
	src := "## Заголовок\n\n*b* и **c** и `d` и ~~e~~\n\n- x\n- y\n\n[л](https://e.example)\n\n```\ncode & <ok>\n```\n"
	parts := renderSlack(src)
	if len(parts) != 1 {
		t.Fatalf("parts=%d", len(parts))
	}
	got := parts[0]
	for _, want := range []string{"*Заголовок*", "_b_", "*c*", "`d`", "~e~", "• x", "<https://e.example|л>", "```\ncode & <ok>\n```"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestRenderTelegramImageAltAndNestedList(t *testing.T) {
	src := "Фото: ![альт текст](https://e.example/pic.png)\n\n- внешний\n  - вложенный\n    - глубже\n- назад\n"
	parts, degraded := renderTelegram(src)
	if degraded {
		t.Fatal("plain image alt must not degrade")
	}
	got := strings.Join(parts, "\n")
	for _, want := range []string{"альт текст", "• внешний", "вложенный", "глубже", "• назад"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestRenderSlackTableQuoteAndHTMLBlock(t *testing.T) {
	src := "| A | B |\n|---|---|\n| 1 | 2 |\n\n> строка\n> ещё\n\n<div>x</div>\n"
	parts := renderSlack(src)
	got := strings.Join(parts, "\n")
	for _, want := range []string{"```", "A", "> строка", "> ещё", "<div>x</div>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestJobMappingMatchesJob(t *testing.T) {
	job := Job{ID: "j1", IdempotencyKey: "k1", UserID: "u", Channel: "telegram",
		Trigger: "message", OrganizationID: "org", ActorID: "actor", ScopeID: "scope",
		Envelope: identity.Envelope{Schema: 1, PrincipalID: "p", ExternalIdentityID: "ext",
			ContextID: "ctx", RuntimeID: "rt", ConversationID: "conv",
			DeliveryTargetID: "dt", PolicyVersion: "v1"}}
	m := mappingFromJob(job, time.Now())
	if !m.MatchesJob(job) {
		t.Fatal("own mapping rejected")
	}
	if m.MatchesJob(Job{ID: "j2"}) {
		t.Fatal("foreign job matched")
	}
}

func TestRenderSlackSplitsWithinLimit(t *testing.T) {
	src := strings.Repeat("слово ", 8000)
	parts := renderSlack(src)
	if len(parts) < 2 {
		t.Fatalf("no split: %d", len(parts))
	}
	for i, p := range parts {
		if utf8.RuneCountInString(p) > slackPartLimit {
			t.Fatalf("part %d over slack limit", i)
		}
	}
}

func TestMarkdownDeliverySendsOrderedPartsAndProgress(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	long := strings.Repeat("длинный ответ ", 600)
	if err := g.spool.EnqueueDelivery(Delivery{ID: "job-telegram-1-response", ChatID: 11, Text: long, Format: formatMarkdown}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) < 2 {
		t.Fatalf("parts not sent: %d", len(fake.sent))
	}
	for i, m := range fake.modes {
		if m != "HTML" {
			t.Fatalf("part %d mode %q", i, m)
		}
	}
	done, err := os.ReadDir(filepath.Join(g.spool.root, "outbox", "done"))
	if err != nil || len(done) != 1 {
		t.Fatalf("delivery not completed: %v %v", done, err)
	}
}

func TestMarkdownDeliveryPartialSendPersistsProgress(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{failOn: 2}
	g.api = fake
	long := strings.Repeat("ещё ответ ", 800)
	if err := g.spool.EnqueueDelivery(Delivery{ID: "job-telegram-2-response", ChatID: 11, Text: long, Format: formatMarkdown}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 {
		t.Fatalf("expected one sent part, got %d", len(fake.sent))
	}
	failed, err := os.ReadFile(filepath.Join(g.spool.root, "outbox", "failed", "job-telegram-2-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d Delivery
	if err := json.Unmarshal(failed, &d); err != nil {
		t.Fatal(err)
	}
	if d.SentParts != 1 || d.PartCount < 2 {
		t.Fatalf("progress not persisted: %+v", d)
	}
	// A second delivery must not re-send part 1: progress resumes at SentParts.
	fake.failOn = 0
	if err := os.Rename(
		filepath.Join(g.spool.root, "outbox", "failed", "job-telegram-2-response.json"),
		filepath.Join(g.spool.root, "outbox", "pending", "job-telegram-2-response.json"),
	); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != d.PartCount || fake.sent[0] == fake.sent[1] && d.PartCount == 2 {
		t.Fatalf("resume sent wrong parts: %d", len(fake.sent))
	}
	if first := fake.sent[0]; strings.Contains(strings.Join(fake.sent[1:], ""), first) {
		t.Fatal("part 1 duplicated after resume")
	}
}

func TestPlainDeliveryStaysPlain(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.queueDelivery(context.Background(), "k", 11, "Варианты: <choice> и **не маркдаун**"); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 || fake.sent[0] != "Варианты: <choice> и **не маркдаун**" {
		t.Fatalf("plain text altered: %v", fake.sent)
	}
	if len(fake.modes) != 1 || fake.modes[0] != "" {
		t.Fatalf("plain delivery got a parse mode: %v", fake.modes)
	}
}

func TestMarkdownDeliverySendsSourceDocWhenDegraded(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	src := "Ответ:\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	if err := g.spool.EnqueueDelivery(Delivery{ID: "job-telegram-3-response", JobID: "j3", ChatID: 11, Text: src, Format: formatMarkdown}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "<pre>") {
		t.Fatalf("rendered table missing: %v", fake.sent)
	}
	if len(fake.docs) != 1 || !strings.HasPrefix(fake.docs[0], "answer.md\x00") || !strings.Contains(fake.docs[0], "| a | b |") {
		t.Fatalf("source doc missing: %v", fake.docs)
	}
}

func TestQuoteWithNestedListAndBreak(t *testing.T) {
	// Blockquote children flatten to plain lines — exercises blockPlainText's
	// list/table/hr branches instead of silently dropping the structure.
	src := "> тезис\n>\n> - один\n> - два\n>\n> ---\n"
	parts, degraded := renderTelegram(src)
	if degraded || len(parts) != 1 {
		t.Fatalf("parts=%d degraded=%v", len(parts), degraded)
	}
	for _, want := range []string{"тезис", "один", "два"} {
		if !strings.Contains(parts[0], want) {
			t.Fatalf("quote lost %q: %s", want, parts[0])
		}
	}
	sparts := renderSlack(src)
	if !strings.Contains(sparts[0], "один") {
		t.Fatalf("slack quote: %v", sparts)
	}
}
