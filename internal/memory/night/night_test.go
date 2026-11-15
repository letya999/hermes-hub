package night

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// fixture builds a vault, then records entries through the real capture
// writer so prov.db and the leaf agree (same production shape).
func fixture(t *testing.T, entries []memory.Entry) string {
	t.Helper()
	return fixtureTZ(t, entries, time.UTC)
}

// fixtureTZ builds the vault with the daily leaf's day boundary in tz —
// the production shape, where capture and rollup share the space clock.
func fixtureTZ(t *testing.T, entries []memory.Entry, tz *time.Location) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	daily, err := vault.OpenDaily(root, tz)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := daily.Append(e, "telegram_bot"); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

var dayNoon = time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

// entryHash computes the block hash the day cache records for an entry.
func entryHash(t *testing.T, root, dayKey, id string) string {
	t.Helper()
	leaf, err := os.ReadFile(filepath.Join(root, "memory", "daily", dayKey+".md"))
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex([]byte(leafBlock(string(leaf), id)))
}

func eligibleEntry(id, body string) memory.Entry {
	return memory.Entry{ID: id, Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
		Audience: memory.AudiencePrivate, Body: body, ObservedAt: dayNoon}
}

// R5.3 red-team: an untrusted day's content never reaches the proposal
// prompt and a fact quoting it can never verify — it is rejected.
func TestPromotionGateExcludesUntrusted(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
		{ID: "turn-2", Kind: memory.KindInteractive, Provenance: memory.ProvenanceUntrusted,
			Audience: memory.AudiencePrivate, Body: "FORGED promotion material xq7",
			ObservedAt: dayNoon},
	})
	var prompt string
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			prompt = user
			return `{"summary":"tea day","facts":[{"card":"notes/tea","fact":"forged claim","quote":"FORGED promotion material xq7"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "FORGED promotion material") {
		t.Fatalf("untrusted body reached the model prompt: %q", prompt)
	}
	if res.Facts != 0 {
		t.Fatalf("a fact sourced from untrusted content promoted: %+v", res)
	}
}

// R5.2(a): the run writes the day's folded summary card with the model's
// summary text.
func TestDaySummarySeeded(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"Owner settled on earl grey as the daily tea.","facts":[{"card":"notes/tea","fact":"owner drinks earl grey","quote":"I only drink earl grey tea"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "daily", "2026-04-19.md"))
	if err != nil {
		t.Fatalf("no day summary written: %v", err)
	}
	if !strings.Contains(string(data), "earl grey as the daily tea") {
		t.Fatalf("summary text missing from folded card: %q", data)
	}
	if res.Facts != 1 {
		t.Fatalf("verified fact not counted: %+v", res)
	}
	card, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	if !strings.Contains(string(card), "- 2026-04-19: owner drinks earl grey") {
		t.Fatalf("dated fact line missing: %q", card)
	}
}

// Live-observed model shape: chat-completion relays wrap the JSON
// proposal in ```json fences. The rollup must lenient-parse it instead
// of failing the day and burning a model call on every tick forever.
func TestProposalWrappedInCodeFence(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "the harbor project uses a blue door on the boathouse"),
	})
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return "```json\n{\"summary\":\"The owner noted the boathouse door.\",\"facts\":[{\"card\":\"notes/harbor\",\"fact\":\"boathouse door is blue\",\"quote\":\"the harbor project uses a blue door on the boathouse\"}]}\n```", nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts != 1 {
		t.Fatalf("fenced proposal not parsed: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", "2026-04-19.md")); err != nil {
		t.Fatalf("no day summary written: %v", err)
	}
}

// R5.3: background-kind entries (cron/assignment/probe/system) are
// promotion-ineligible — they never reach the prompt or a fact's quote.
func TestPromotionGateExcludesKinds(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "the standup moved to nine"),
		{ID: "cron-1", Kind: memory.KindRoutine, Provenance: memory.ProvenanceAgent,
			Audience: memory.AudiencePrivate, Body: "ROUTINE-NIGHTLY secret sweep content",
			ObservedAt: dayNoon},
		{ID: "compact-1", Kind: memory.KindCompaction, Provenance: memory.ProvenanceAgent,
			Audience: memory.AudiencePrivate, Body: "COMPACTION dropped context",
			ObservedAt: dayNoon},
	})
	var prompt string
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			prompt = user
			return `{"summary":"quiet day","facts":[]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"ROUTINE-NIGHTLY", "COMPACTION dropped"} {
		if strings.Contains(prompt, leak) {
			t.Fatalf("background-kind content %q reached the prompt: %q", leak, prompt)
		}
	}
}

// R5.2(a): a fact whose evidence quote is not verbatim leaf text is
// rejected — a subtly mutated quote cannot launder a claim.
func TestMutatedQuoteRejected(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea","facts":[{"card":"notes/tea","fact":"owner drinks green tea","quote":"I only drink green tea"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts != 0 || res.Rejected != 1 {
		t.Fatalf("mutated-quote fact promoted: %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	if strings.Contains(string(data), "green tea") {
		t.Fatalf("rejected fact landed in a card: %q", data)
	}
}

// R5.2(c): a proposed link is written on both cards — [[a]] on b and
// [[b]] on a — and only when both targets already exist.
func TestBilateralLinkBothExist(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	cardBody := "---\ntype: note\nstatus: active\ntags: []\ncreated: 2026-04-19\nupdated: 2026-04-19\n---\n\n## Facts\n"
	for _, name := range []string{"tea", "earl-grey"} {
		if err := os.MkdirAll(filepath.Join(root, "memory", "cards", "notes"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "memory", "cards", "notes", name+".md"), []byte(cardBody), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea","facts":[],"card_refs":["tea"],"links":[["tea","earl-grey"],["tea","ghost-card"]]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	tea, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	earl, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "earl-grey.md"))
	if !strings.Contains(string(tea), "[[earl-grey]]") || !strings.Contains(string(earl), "[[tea]]") {
		t.Fatalf("bilateral link missing: tea=%q earl=%q", tea, earl)
	}
	if strings.Contains(string(tea), "[[ghost-card]]") {
		t.Fatalf("link to a missing card landed: %q", tea)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "cards", "notes", "ghost-card.md")); !os.IsNotExist(err) {
		t.Fatalf("link proposal minted a card: %v", err)
	}
}

// R5.5: a new directive on the same key replaces the dated line in place —
// preferences supersede, they do not accumulate.
func TestDirectiveSupersedeInPlace(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "actually I switched — earl grey from now on"),
	})
	core := "# CORE\n\n## Directives\n- 2026-04-10 [preference] beverage: prefers coffee\n\n## Facts\n- owner likes tea\n"
	if err := os.WriteFile(filepath.Join(root, "memory", "CORE.md"), []byte(core), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea","facts":[],"core":{"directives":[{"key":"beverage","class":"preference","text":"prefers earl grey","quote":"earl grey from now on"}]}}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "memory", "CORE.md"))
	body := string(data)
	if strings.Contains(body, "prefers coffee") {
		t.Fatalf("superseded directive survived: %q", body)
	}
	if !strings.Contains(body, "beverage: prefers earl grey") || !strings.Contains(body, "2026-04-19") {
		t.Fatalf("new dated directive missing: %q", body)
	}
}

// R5.5: health/safety directives are never-evict — a same-key proposal is
// rejected and the pinned line stands.
func TestDirectiveNeverEvict(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "stop the morning insulin routine"),
	})
	core := "# CORE\n\n## Directives\n- 2026-04-10 [health] medication: insulin every morning\n\n## Facts\n"
	if err := os.WriteFile(filepath.Join(root, "memory", "CORE.md"), []byte(core), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"health","facts":[],"core":{"directives":[{"key":"medication","class":"preference","text":"stop insulin","quote":"stop the morning insulin routine"}]}}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "memory", "CORE.md"))
	body := string(data)
	if !strings.Contains(body, "insulin every morning") || strings.Contains(body, "stop insulin") {
		t.Fatalf("never-evict directive mutated: %q", body)
	}
	if res.Rejected != 1 {
		t.Fatalf("never-evict replacement not rejected: %+v", res)
	}
}

// R5.2(d): a CORE fact lands as a dated line under ## Facts — and only
// behind a verbatim owner quotation (agent text is not enough).
func TestCoreFactNeedsOwnerQuote(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
		{ID: "reply-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceAgent,
			Audience: memory.AudiencePrivate, Body: "noted — earl grey it is",
			ObservedAt: dayNoon},
	})
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea","facts":[],"core":{"facts":[
				{"text":"owner prefers earl grey","quote":"I only drink earl grey tea"},
				{"text":"agent remembers tea","quote":"noted — earl grey it is"}]}}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "memory", "CORE.md"))
	text := string(body)
	if !strings.Contains(text, "- 2026-04-19: owner prefers earl grey") {
		t.Fatalf("owner-quoted core fact missing: %q", text)
	}
	if strings.Contains(text, "agent remembers tea") {
		t.Fatalf("agent-quoted core fact promoted: %q", text)
	}
}

// R5.4: a truth rewrite that would drop more than the bounded share of
// existing entries is rejected — prior state survives and the proposal's
// genuinely new lines still land append-only.
func TestBoundedLossRejects(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	card := "---\ntype: note\nstatus: active\ntags: []\ncreated: 2026-04-10\nupdated: 2026-04-10\n---\n\n## Facts\n\n- keep-one\n- keep-two\n- keep-three\n- keep-four\n- keep-five\n"
	if err := os.MkdirAll(filepath.Join(root, "memory", "cards", "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"), []byte(card), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea","facts":[],"truths":[{"card":"notes/tea","content":"---\ntype: note\nstatus: active\ntags: []\ncreated: 2026-04-10\nupdated: 2026-04-19\n---\n\n## Facts\n\n- keep-one\n- brand-new-line\n"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	text := string(body)
	for _, keep := range []string{"keep-two", "keep-three", "keep-four", "keep-five"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("bounded-loss rewrite dropped %q: %q", keep, text)
		}
	}
	if !strings.Contains(text, "brand-new-line") {
		t.Fatalf("append-only fallback lost the new line: %q", text)
	}
	if res.Rejected == 0 {
		t.Fatalf("lossy rewrite was not rejected: %+v", res)
	}
}

// R5.4/D5: a rewrite within the loss bound applies — and the replaced body
// is preserved under the card's ## History section.
func TestBoundedLossApplies(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	card := "---\ntype: note\nstatus: active\ntags: []\ncreated: 2026-04-10\nupdated: 2026-04-10\n---\n\n## Facts\n\n- keep-one\n- keep-two\n- keep-three\n- keep-four\n- keep-five\n"
	if err := os.MkdirAll(filepath.Join(root, "memory", "cards", "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"), []byte(card), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea","facts":[],"truths":[{"card":"notes/tea","content":"---\ntype: note\nstatus: active\ntags: []\ncreated: 2026-04-10\nupdated: 2026-04-19\n---\n\n## Facts\n\n- keep-one\n- keep-two\n- keep-three\n- keep-four\n- merged-five\n"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	text := string(body)
	if !strings.Contains(text, "- merged-five") {
		t.Fatalf("in-bound rewrite did not apply: %q", text)
	}
	if !strings.Contains(text, "## History") || !strings.Contains(text, "- keep-five") {
		t.Fatalf("replaced body was not preserved in history: %q", text)
	}
	if res.Rejected != 0 {
		t.Fatalf("in-bound rewrite was rejected: %+v", res)
	}
}

// R8.4: an owner edit on disk — the OK UI path — is swept into history as
// hermes-owner before the rollup's own writes, so the run's card commit can
// never absorb owner bytes under a system author. The edit itself survives
// the rewrite (preserved under ## History like any replaced body).
func TestUIEditWinsRollup(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	card := "---\ntype: note\nstatus: active\ntags: []\ncreated: 2026-04-10\nupdated: 2026-04-10\n---\n\n## Facts\n\n- owner-pinned-line\n"
	if err := os.MkdirAll(filepath.Join(root, "memory", "cards", "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"), []byte(card), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea","facts":[],"truths":[{"card":"notes/tea","content":"---\ntype: note\nstatus: active\ntags: []\ncreated: 2026-04-10\nupdated: 2026-04-19\n---\n\n## Facts\n\n- rollup-line\n"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	if !strings.Contains(string(body), "owner-pinned-line") {
		t.Fatalf("owner line dropped from the card: %q", body)
	}
	cmd := exec.Command("git", "log", "--format=%an", "--", "memory/cards/notes/tea.md")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	authors := strings.Fields(string(out))
	if len(authors) < 2 || authors[1] != "hermes-owner" {
		t.Fatalf("owner edit must commit as hermes-owner before the rollup write: %v", authors)
	}
}

// R5.6: every run appends a dated REVIEW.md section recording each
// promotion, skip and rejection with its reason — human-readable.
func TestReviewJournalEntries(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea day","facts":[{"card":"notes/tea","fact":"prefers earl grey","quote":"I only drink earl grey tea"},{"card":"notes/x","fact":"forged","quote":"never said this"}],"links":[["notes/tea","notes/ghost"]]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "REVIEW.md"))
	if err != nil {
		t.Fatalf("REVIEW.md missing: %v", err)
	}
	text := string(body)
	for _, want := range []string{"2026-04-19", "notes/tea", "forged", "notes/ghost"} {
		if !strings.Contains(text, want) {
			t.Fatalf("journal missing %q:\n%s", want, text)
		}
	}
}

// R5.4+R5.6: a truth rewrite rejected by the bounded-loss gate leaves a
// journal line explaining the rejection — the owner can see what the
// model wanted to drop.
func TestTruthLossJournaled(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	card := "---\ntype: note\n---\n\n## Facts\n\n- fact one\n- fact two\n- fact three\n- fact four\n- fact five\n"
	if err := os.MkdirAll(filepath.Join(root, "memory", "cards", "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"), []byte(card), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea day","truths":[{"card":"notes/tea","content":"---\ntype: note\n---\n\n## Facts\n\n- only this\n"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "REVIEW.md"))
	if err != nil {
		t.Fatalf("REVIEW.md missing: %v", err)
	}
	if !strings.Contains(string(body), "notes/tea") {
		t.Fatalf("journal missing the rejected truth's card:\n%s", body)
	}
}

// R5.7+R5.6: an intent whose quote cannot be verified is rejected and
// the journal says so — proposal traffic is auditable, not silent.
func TestRejectedIntentJournaled(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "call the bank Friday"),
	})
	sinkCalls := 0
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"errands","intents":[{"text":"buy a boat","quote":"never said this"}]}`, nil
		},
		IntentSink: func(ctx context.Context, p IntentProposal) error {
			sinkCalls++
			return nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if sinkCalls != 0 {
		t.Fatalf("forged intent reached the sink %d times", sinkCalls)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "REVIEW.md"))
	if err != nil {
		t.Fatalf("REVIEW.md missing: %v", err)
	}
	if !strings.Contains(string(body), "buy a boat") {
		t.Fatalf("journal missing the rejected intent:\n%s", body)
	}
}

// R5.6: the "what I remembered" digest summarizes what actually landed —
// it is produced by the run and rides the owner-brief path when queued.
func TestDigestQueued(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea day","facts":[{"card":"notes/tea","fact":"prefers earl grey","quote":"I only drink earl grey tea"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Digest, "prefers earl grey") {
		t.Fatalf("digest misses the promoted fact: %q", res.Digest)
	}
}

// R5.7: a "remind me" intent becomes an owner-approval proposal through
// the injected sink — never a fact, never auto-activated.
func TestIntentBecomesProposal(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "remind me Friday to call the bank"),
	})
	var got []IntentProposal
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"errands","intents":[{"text":"call the bank Friday","quote":"remind me Friday to call the bank"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
		IntentSink: func(ctx context.Context, p IntentProposal) error {
			got = append(got, p)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Text, "call the bank") {
		t.Fatalf("intent did not reach the sink: %+v", got)
	}
}

// R5.7: an intent without a verbatim eligible quote never reaches the
// sink — intents can no more be forged than facts.
func TestIntentNeverFact(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "remind me Friday to call the bank"),
	})
	var got []IntentProposal
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"errands","intents":[{"text":"buy crypto","quote":"owner never said this"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
		IntentSink: func(ctx context.Context, p IntentProposal) error {
			got = append(got, p)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unquoted intent reached the sink: %+v", got)
	}
	if res.Rejected == 0 {
		t.Fatalf("unquoted intent was not rejected: %+v", res)
	}
}

// R5.8: the idempotent day cache makes a second run for the same day a
// no-op — no relay call, no new writes.
func TestSecondRunNoop(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	calls := 0
	opts := Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			calls++
			return `{"summary":"tea day","facts":[{"card":"notes/tea","fact":"prefers earl grey","quote":"I only drink earl grey tea"}]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	}
	if _, err := Run(context.Background(), root, dayNoon, opts); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), root, dayNoon, opts)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("second run re-called the model: %d calls", calls)
	}
	if res.Facts != 0 {
		t.Fatalf("second run re-applied facts: %+v", res)
	}
}

// R5.8: resuming a partial day whose recorded entry content changed
// halts the run — silently mixing evidence is worse than stopping.
func TestChangedEntryHalts(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	cache := filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	seed := `{"entries":{"turn-1":"deadbeef"},"steps":{"facts":true}}`
	if err := os.WriteFile(cache, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"x"}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != "halt" {
		t.Fatalf("changed entry did not halt the day: %+v", res)
	}
}

// R5.8: a crashed run resumes from its last completed step using the
// cached proposal — the model is not re-called.
func TestResumeFromStep(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	if err := os.MkdirAll(filepath.Join(root, "memory", "cards", "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"), []byte("---\ntype: note\n---\n\n## Facts\n\n- prior\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	entrySHA := entryHash(t, root, "2026-04-19", "turn-1")
	prop := `{"summary":"tea day","facts":[{"card":"notes/tea","fact":"prefers earl grey","quote":"I only drink earl grey tea"}],"links":[["notes/tea","notes/tea"]]}`
	seed := fmt.Sprintf(`{"entries":{"turn-1":%q},"proposal":%s,"steps":{"facts":true}}`, entrySHA, prop)
	if err := os.WriteFile(cache, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			calls++
			return `{"summary":"should not be called"}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("resume re-called the model: %d calls", calls)
	}
	if res.SummaryMD != "tea day" {
		t.Fatalf("cached proposal was not reused: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", "2026-04-19.md")); err != nil {
		t.Fatalf("resume did not finish the remaining steps: %v", err)
	}
	// steps.facts was recorded done — resume must not re-append the fact.
	body, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	if strings.Count(string(body), "prefers earl grey") != 0 {
		t.Fatalf("completed facts step re-applied on resume: %q", body)
	}
	// A self-link is meaningless — no ## Links section may be created.
	if strings.Contains(string(body), "## Links") {
		t.Fatalf("self-link wrote a links section: %q", body)
	}
}

// R5.8: an estimated input-token cost over the night budget stops the
// run cleanly — before the model call, before any write.
func TestBudgetStopsClean(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	calls := 0
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			calls++
			return `{"summary":"x"}`, nil
		},
		Now:            dayNoon.Add(24 * time.Hour),
		MaxInputTokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("over-budget run still called the model: %d calls", calls)
	}
	if res.Stopped != "budget" {
		t.Fatalf("over-budget run did not stop as budget: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", "2026-04-19.md")); !os.IsNotExist(err) {
		t.Fatal("over-budget run wrote a summary")
	}
}

// R5.1/R5.9 edge: day assignment follows the owner timezone across a DST
// boundary — an entry just before local midnight belongs to the prior
// day even though UTC has already rolled.
func TestDSTEdge(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("tz data unavailable")
	}
	root := fixtureTZ(t, []memory.Entry{
		// 2026-03-28T22:30Z = 23:30 CET on 2026-03-28 in Berlin.
		{ID: "before", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
			Audience: memory.AudiencePrivate, Body: "pre-midnight tea",
			ObservedAt: time.Date(2026, 3, 28, 22, 30, 0, 0, time.UTC)},
		// 2026-03-28T23:30Z = 00:30 CET on 2026-03-29 — DST day.
		{ID: "after", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
			Audience: memory.AudiencePrivate, Body: "post-midnight coffee",
			ObservedAt: time.Date(2026, 3, 28, 23, 30, 0, 0, time.UTC)},
	}, berlin)
	var prompt string
	day := time.Date(2026, 3, 29, 4, 0, 0, 0, berlin)
	_, err = Run(context.Background(), root, day, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			prompt = user
			return `{"summary":"coffee","facts":[]}`, nil
		},
		TZ:  berlin,
		Now: day.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "pre-midnight tea") {
		t.Fatalf("prior-day entry leaked into the 2026-03-29 rollup: %q", prompt)
	}
	if !strings.Contains(prompt, "post-midnight coffee") {
		t.Fatalf("post-midnight entry missing from its day: %q", prompt)
	}
}

// A dry run evaluates the proposal and reports intent — the vault,
// journal and day cache stay untouched.
func TestDryRunWritesNothing(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"tea day","facts":[{"card":"notes/tea","fact":"prefers earl grey","quote":"I only drink earl grey tea"}]}`, nil
		},
		Now:    dayNoon.Add(24 * time.Hour),
		DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(root, "memory", "cards", "notes", "tea.md"),
		filepath.Join(root, "memory", "REVIEW.md"),
		filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote %s", p)
		}
	}
}

// R5.9: a weekly fold needs all seven day states — a day with no
// captured entries counts through its recorded no-data state, but a day
// the rollup never processed blocks the fold.
func TestWeeklyNeedsSeven(t *testing.T) {
	root := fixture(t, nil)
	monday := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC) // ISO week start
	// Seed six of the seven day states — Friday is missing.
	for i := 0; i < 7; i++ {
		if i == 4 {
			continue
		}
		d := monday.AddDate(0, 0, i).Format("2006-01-02")
		cache := filepath.Join(root, "runtime", "memory", "night", d+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := FoldWeek(context.Background(), root, monday, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"week"}`, nil
		},
		Now: monday.AddDate(0, 0, 8),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != "incomplete" {
		t.Fatalf("fold ran with a missing day state: %+v", res)
	}
}

// R5.9: with all seven terminal day states — no-data days folded as
// markers — the weekly summary lands under memory/summaries/weekly/.
func TestWeeklyFoldsAllSeven(t *testing.T) {
	root := fixture(t, nil)
	monday := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		d := monday.AddDate(0, 0, i).Format("2006-01-02")
		cache := filepath.Join(root, "runtime", "memory", "night", d+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// One day carries a real summary — the fold prompt must contain it;
	// the other six are no-data markers.
	sumDir := filepath.Join(root, "memory", "summaries", "daily")
	if err := os.MkdirAll(sumDir, 0700); err != nil {
		t.Fatal(err)
	}
	dayFile := "---\ntype: daily-summary\n---\n\nOwner settled the tea question.\n"
	if err := os.WriteFile(filepath.Join(sumDir, "2026-04-14.md"), []byte(dayFile), 0600); err != nil {
		t.Fatal(err)
	}
	var prompt string
	res, err := FoldWeek(context.Background(), root, monday, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			prompt = user
			return `{"summary":"quiet week","facts":[]}`, nil
		},
		Now: monday.AddDate(0, 0, 8),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != "" {
		t.Fatalf("fold stopped: %+v", res)
	}
	if !strings.Contains(prompt, "settled the tea question") {
		t.Fatalf("day summary did not reach the fold prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "(no data)") {
		t.Fatalf("no-data day marker missing from prompt: %q", prompt)
	}
	weekly := filepath.Join(root, "memory", "summaries", "weekly", "2026-W16.md")
	body, err := os.ReadFile(weekly)
	if err != nil {
		t.Fatalf("weekly summary missing: %v", err)
	}
	if !strings.Contains(string(body), "quiet week") {
		t.Fatalf("weekly summary wrong: %q", body)
	}
}

// The fold seams share the day rollup's relay contract — a fenced
// proposal must parse here too, or every night's fold sweep burns a
// model call and retries forever.
func TestWeeklyFoldWrappedInCodeFence(t *testing.T) {
	root := fixture(t, nil)
	monday := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		d := monday.AddDate(0, 0, i).Format("2006-01-02")
		cache := filepath.Join(root, "runtime", "memory", "night", d+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := FoldWeek(context.Background(), root, monday, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return "```json\n{\"summary\":\"fenced week\",\"facts\":[]}\n```", nil
		},
		Now: monday.AddDate(0, 0, 8),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "weekly", "2026-W16.md"))
	if err != nil {
		t.Fatalf("weekly summary missing: %v", err)
	}
	if !strings.Contains(string(body), "fenced week") {
		t.Fatalf("weekly summary wrong: %q", body)
	}
}

// R5.8: a resumed run keeps the pre-crash journal lines and digest —
// the day's REVIEW section and the owner brief reflect every promotion
// the day made, not only the resume's slice.
func TestResumeKeepsJournalAndDigest(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	cache := filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	entrySHA := entryHash(t, root, "2026-04-19", "turn-1")
	seed := fmt.Sprintf(`{"entries":{"turn-1":%q},"proposal":{"summary":"tea day","facts":[]},`+
		`"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true},`+
		`"journal":["promoted fact on notes/tea: prefers earl grey"],"digest":"- prefers earl grey"}`, entrySHA)
	if err := os.WriteFile(cache, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			t.Fatal("resume must not re-call the model")
			return "", nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest != "- prefers earl grey" {
		t.Fatalf("resume lost the pre-crash digest: %q", res.Digest)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "REVIEW.md"))
	if err != nil {
		t.Fatalf("resume wrote no journal: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "promoted fact on notes/tea") {
		t.Fatalf("resume lost pre-crash journal lines: %q", text)
	}
	if strings.Count(text, "## 2026-04-19") != 1 {
		t.Fatalf("journal wrote more than one day section: %q", text)
	}
}

// R5.8: a crash between the journal write and its checkpoint leaves the
// day's section already on disk — resume must not append a duplicate.
func TestJournalNotDuplicatedOnResume(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	cache := filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	entrySHA := entryHash(t, root, "2026-04-19", "turn-1")
	seed := fmt.Sprintf(`{"entries":{"turn-1":%q},"proposal":{"summary":"tea day","facts":[]},`+
		`"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true}}`, entrySHA)
	if err := os.WriteFile(cache, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "REVIEW.md"), []byte("# REVIEW — nightly rollup journal\n\n## 2026-04-19\n\n- promoted fact on notes/tea: prefers earl grey\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			t.Fatal("resume must not re-call the model")
			return "", nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "memory", "REVIEW.md"))
	if got := strings.Count(string(body), "## 2026-04-19"); got != 1 {
		t.Fatalf("journal duplicated the day section on resume (%d):\n%s", got, body)
	}
}

// R5.8: completed stages are not re-applied on resume — seeded-done
// links, intents and core leave the cards, the sink and CORE untouched.
func TestResumeSkipsDoneStages(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	cardBody := "---\ntype: note\n---\n\n## Facts\n\n- prior\n"
	for _, name := range []string{"tea", "earl-grey"} {
		if err := os.MkdirAll(filepath.Join(root, "memory", "cards", "notes"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "memory", "cards", "notes", name+".md"), []byte(cardBody), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "CORE.md"), []byte("# CORE\n\n## Directives\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	entrySHA := entryHash(t, root, "2026-04-19", "turn-1")
	prop := `{"summary":"tea day","facts":[],"links":[["tea","earl-grey"]],` +
		`"intents":[{"text":"call the bank","quote":"I only drink earl grey tea"}],` +
		`"core":{"directives":[{"key":"beverage","class":"preference","text":"earl grey","quote":"I only drink earl grey tea"}]}}`
	seed := fmt.Sprintf(`{"entries":{"turn-1":%q},"proposal":%s,`+
		`"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true}}`, entrySHA, prop)
	if err := os.WriteFile(cache, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	sinkCalls := 0
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			t.Fatal("resume must not re-call the model")
			return "", nil
		},
		Now: dayNoon.Add(24 * time.Hour),
		IntentSink: func(ctx context.Context, p IntentProposal) error {
			sinkCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sinkCalls != 0 {
		t.Fatalf("done intents stage re-fired the sink: %d", sinkCalls)
	}
	tea, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	if strings.Contains(string(tea), "## Links") {
		t.Fatalf("done links stage re-applied: %q", tea)
	}
	core, _ := os.ReadFile(filepath.Join(root, "memory", "CORE.md"))
	if strings.Contains(string(core), "beverage: earl grey") {
		t.Fatalf("done core stage re-applied: %q", core)
	}
}

// R5.8: the call cap is cumulative across a night's resume cycles —
// a cache that already spent its calls must not consult the model
// again for an un-cached proposal.
func TestMaxCallsStopsBeforeRelay(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	cache := filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	entrySHA := entryHash(t, root, "2026-04-19", "turn-1")
	// Crash after the facts stage but before any proposal was cached —
	// the model was already called once, which is the whole budget.
	seed := fmt.Sprintf(`{"entries":{"turn-1":%q},"calls":1,"steps":{"facts":true}}`, entrySHA)
	if err := os.WriteFile(cache, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	res, err := Run(context.Background(), root, dayNoon, Options{
		MaxCalls: 1,
		Relay: func(ctx context.Context, system, user string) (string, error) {
			called = true
			return `{}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("relay called after the call budget was spent")
	}
	if res.Stopped != "budget" {
		t.Fatalf("Stopped = %q, want budget", res.Stopped)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", "2026-04-19.md")); !os.IsNotExist(err) {
		t.Fatal("stopped run wrote a daily summary")
	}
}

// R5.8: a failed relay call still spent its budget — the day state
// records the call so a retry-resume sees the remaining budget, not
// the original cap.
func TestFailedRelayStillCountsSpend(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	relayErr := func(ctx context.Context, system, user string) (string, error) {
		return "", fmt.Errorf("relay down")
	}
	if _, err := Run(context.Background(), root, dayNoon, Options{
		MaxCalls: 1,
		Relay:    relayErr,
		Now:      dayNoon.Add(24 * time.Hour),
	}); err == nil {
		t.Fatal("failing relay returned nil error")
	}
	called := false
	res, err := Run(context.Background(), root, dayNoon, Options{
		MaxCalls: 1,
		Relay: func(ctx context.Context, system, user string) (string, error) {
			called = true
			return `{}`, nil
		},
		Now: dayNoon.Add(25 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("retry burned a second call past the cap")
	}
	if res.Stopped != "budget" {
		t.Fatalf("Stopped = %q, want budget", res.Stopped)
	}
}

// Dry-run touches nothing — not even the spend ledger a failed call
// would otherwise persist for the resume budget (R5.8).
func TestDryRunFailedRelayWritesNoCache(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	_, err := Run(context.Background(), root, dayNoon, Options{
		DryRun: true,
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return "", fmt.Errorf("relay down")
		},
		Now: dayNoon.Add(24 * time.Hour),
	})
	if err == nil {
		t.Fatal("failing relay returned nil error")
	}
	if _, err := os.Stat(filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote the day cache")
	}
}

// R5.9: a month folds only once every in-month day reached a terminal
// state — an unprocessed day blocks the monthly summary entirely.
func TestMonthlyNeedsCompleteMonth(t *testing.T) {
	root := fixture(t, nil)
	seedDoneDay := func(d time.Time) {
		key := d.Format("2006-01-02")
		cache := filepath.Join(root, "runtime", "memory", "night", key+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// April 2026 has 30 days; seed all but the last.
	first := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	for d := 0; d < 29; d++ {
		seedDoneDay(first.AddDate(0, 0, d))
	}
	res, err := FoldMonth(context.Background(), root, first, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			t.Fatal("incomplete month must not call the model")
			return "", nil
		},
		Now: first.AddDate(0, 1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != "incomplete" {
		t.Fatalf("Stopped = %q, want incomplete", res.Stopped)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "monthly", "2026-04.md")); !os.IsNotExist(err) {
		t.Fatal("monthly summary written on an incomplete month")
	}
	// The last day completes the month — the fold runs and lands.
	seedDoneDay(first.AddDate(0, 0, 29))
	res, err = FoldMonth(context.Background(), root, first, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"april rollup","facts":[]}`, nil
		},
		Now: first.AddDate(0, 1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "monthly", "2026-04.md")); err != nil {
		t.Fatalf("complete month wrote no summary: %v", err)
	}
}

// The monthly fold shares the relay contract — fenced JSON must parse.
func TestMonthlyFoldWrappedInCodeFence(t *testing.T) {
	root := fixture(t, nil)
	first := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	for d := 0; d < 30; d++ {
		key := first.AddDate(0, 0, d).Format("2006-01-02")
		cache := filepath.Join(root, "runtime", "memory", "night", key+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := FoldMonth(context.Background(), root, first, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return "```json\n{\"summary\":\"fenced april\",\"facts\":[]}\n```", nil
		},
		Now: first.AddDate(0, 1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "monthly", "2026-04.md"))
	if err != nil {
		t.Fatalf("monthly summary missing: %v", err)
	}
	if !strings.Contains(string(body), "fenced april") {
		t.Fatalf("monthly summary wrong: %q", body)
	}
}

// R5.9: the monthly prompt composes full in-month weeks from their
// weekly summaries — the covered days are not repeated as dailies —
// and only the partial-week edge days land as daily material.
func TestMonthlyComposesWeeksPlusEdges(t *testing.T) {
	root := fixture(t, nil)
	seedDoneDay := func(d time.Time) {
		key := d.Format("2006-01-02")
		cache := filepath.Join(root, "runtime", "memory", "night", key+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	for d := 0; d < 30; d++ {
		seedDoneDay(first.AddDate(0, 0, d))
	}
	write := func(rel, text string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\ntype: x\n---\n\n"+text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// April 2026: full ISO weeks W15–W17 inside the month; Apr 1–5 and
	// Apr 27–30 are edge days of partial weeks.
	for _, wk := range []string{"2026-W15", "2026-W16", "2026-W17"} {
		write("memory/summaries/weekly/"+wk+".md", "weekly "+wk)
	}
	write("memory/summaries/daily/2026-04-01.md", "edge day one")
	write("memory/summaries/daily/2026-04-30.md", "edge day last")
	write("memory/summaries/daily/2026-04-10.md", "SHOULD NOT APPEAR — covered by W15")
	var prompt string
	_, err := FoldMonth(context.Background(), root, first, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			prompt = user
			return `{"summary":"april","facts":[]}`, nil
		},
		Now: first.AddDate(0, 1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### 2026-W15", "### 2026-W16", "### 2026-W17", "### 2026-04-01\n", "edge day one", "### 2026-04-30\n", "edge day last"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("monthly prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "2026-04-10") || strings.Contains(prompt, "SHOULD NOT APPEAR") {
		t.Fatalf("in-week day leaked into the monthly prompt:\n%s", prompt)
	}
}

// R5.9: the year fold requires all twelve monthly summaries — a missing
// month blocks the yearly summary entirely.
func TestYearlyNeedsTwelveMonths(t *testing.T) {
	root := fixture(t, nil)
	seedMonth := func(m time.Month) {
		dir := filepath.Join(root, "memory", "summaries", "monthly")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, fmt.Sprintf("2026-%02d.md", int(m)))
		if err := os.WriteFile(path, []byte("---\ntype: monthly-summary\n---\n\nmonth\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for m := time.January; m < time.December; m++ {
		seedMonth(m)
	}
	day := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	res, err := FoldYear(context.Background(), root, day, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			t.Fatal("missing month must not call the model")
			return "", nil
		},
		Now: time.Date(2027, 1, 5, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != "incomplete" {
		t.Fatalf("Stopped = %q, want incomplete", res.Stopped)
	}
	seedMonth(time.December)
	res, err = FoldYear(context.Background(), root, day, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"year rollup","facts":[]}`, nil
		},
		Now: time.Date(2027, 1, 5, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "yearly", "2026.md")); err != nil {
		t.Fatalf("complete year wrote no summary: %v", err)
	}
}

// The yearly fold shares the relay contract — fenced JSON must parse.
func TestYearlyFoldWrappedInCodeFence(t *testing.T) {
	root := fixture(t, nil)
	dir := filepath.Join(root, "memory", "summaries", "monthly")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for m := 1; m <= 12; m++ {
		path := filepath.Join(dir, fmt.Sprintf("2026-%02d.md", m))
		if err := os.WriteFile(path, []byte("---\ntype: monthly-summary\n---\n\nmonth\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	_, err := FoldYear(context.Background(), root, day, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return "```json\n{\"summary\":\"fenced year\",\"facts\":[]}\n```", nil
		},
		Now: time.Date(2027, 1, 5, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "yearly", "2026.md"))
	if err != nil {
		t.Fatalf("yearly summary missing: %v", err)
	}
	if !strings.Contains(string(body), "fenced year") {
		t.Fatalf("yearly summary wrong: %q", body)
	}
}

// R5.8: the truths, summary and journal stages are step-gated too — a
// resume with them recorded done must not rewrite the card's truth, the
// daily summary, or append the journal section.
func TestResumeSkipsTruthsSummaryJournal(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	card := "---\ntype: note\n---\n\n## Facts\n\n- keep-one\n"
	if err := os.MkdirAll(filepath.Join(root, "memory", "cards", "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"), []byte(card), 0600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	entrySHA := entryHash(t, root, "2026-04-19", "turn-1")
	prop := `{"summary":"RESUMED summary text","facts":[],"truths":[{"card":"notes/tea","content":"---\ntype: note\n---\n\n## Facts\n\n- rewritten\n"}]}`
	seed := fmt.Sprintf(`{"entries":{"turn-1":%q},"proposal":%s,`+
		`"steps":{"facts":true,"truths":true,"summary":true,"journal":true}}`, entrySHA, prop)
	if err := os.WriteFile(cache, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			t.Fatal("resume must not re-call the model")
			return "", nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	tea, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	if strings.Contains(string(tea), "rewritten") || strings.Contains(string(tea), "## History") {
		t.Fatalf("done truths stage re-applied: %q", tea)
	}
	// The summary step is recorded done — the (missing) file stays missing.
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", "2026-04-19.md")); !os.IsNotExist(err) {
		t.Fatal("done summary stage rewrote the daily file")
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "REVIEW.md")); !os.IsNotExist(err) {
		t.Fatal("done journal stage wrote REVIEW.md")
	}
}

// R5.8: a day with no eligible entries completes its day state without a
// model call and without writing a vacuous summary — the fold renders it
// as the "(no data)" marker instead.
func TestNightSkipsEmptyDay(t *testing.T) {
	root := fixture(t, nil)
	called := false
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			called = true
			return `{"summary":"x","facts":[]}`, nil
		},
		Now: dayNoon,
	})
	if err != nil {
		t.Fatal(err)
	}
	if called || res.Calls != 0 {
		t.Fatalf("empty day spent a model call: %+v", res)
	}
	st, err := loadDayState(root, "2026-04-19")
	if err != nil || !st.complete() {
		t.Fatalf("empty day left no terminal state: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", "2026-04-19.md")); !os.IsNotExist(err) {
		t.Fatal("empty day wrote a daily summary")
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "REVIEW.md")); !os.IsNotExist(err) {
		t.Fatal("empty day wrote a journal")
	}
}

// R5.9: a fold whose summary already exists is a no-op — no relay call,
// no rewrite (an owner edit of the summary is never clobbered).
func TestFoldKeepsExistingSummary(t *testing.T) {
	root := fixture(t, nil)
	monday := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		d := monday.AddDate(0, 0, i).Format("2006-01-02")
		cache := filepath.Join(root, "runtime", "memory", "night", d+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "memory", "summaries", "weekly")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	existing := "---\ntype: weekly-summary\n---\n\nowner's weekly edit\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-W16.md"), []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := FoldWeek(context.Background(), root, monday, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			called = true
			return `{"summary":"week"}`, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("fold re-fired over an existing summary")
	}
	body, _ := os.ReadFile(filepath.Join(dir, "2026-W16.md"))
	if string(body) != existing {
		t.Fatalf("existing summary rewritten: %q", body)
	}
	// Monthly and yearly folds hold the same immutability contract — an
	// existing summary short-circuits before states are even consulted.
	// Both folds are made genuinely ready first: April's thirty day
	// states all terminal, and all twelve monthly summaries present.
	for d := 1; d <= 30; d++ {
		key := fmt.Sprintf("2026-04-%02d", d)
		cache := filepath.Join(root, "runtime", "memory", "night", key+".json")
		if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for m := 1; m <= 12; m++ {
		if m == 4 {
			continue // the April file below doubles as FoldYear's input
		}
		abs := filepath.Join(root, "memory", "summaries", "monthly", fmt.Sprintf("2026-%02d.md", m))
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("month\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []struct {
		rel  string
		fold func(context.Context, string, time.Time, Options) (Result, error)
	}{
		{"memory/summaries/monthly/2026-04.md", FoldMonth},
		{"memory/summaries/yearly/2026.md", FoldYear},
	} {
		abs := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("kept\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.fold(context.Background(), root, monday, Options{
			Relay: func(ctx context.Context, system, user string) (string, error) {
				called = true
				return `{"summary":"x"}`, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(abs)
		if string(body) != "kept\n" {
			t.Fatalf("%s rewritten", f.rel)
		}
	}
	if called {
		t.Fatal("a fold re-fired over an existing summary")
	}
}

// R5.6: a truth proposal against a card that does not exist is a skip —
// rejected and journaled, not silently dropped.
func TestMissingTruthTargetJournaled(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "I only drink earl grey tea"),
	})
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			return `{"summary":"s","facts":[],"truths":[{"card":"notes/ghost","content":"- fabricated rewrite\n"}]}`, nil
		},
		Now: dayNoon,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "memory", "REVIEW.md"))
	if err != nil {
		t.Fatalf("REVIEW.md missing: %v", err)
	}
	if !strings.Contains(string(body), "ghost") || !strings.Contains(string(body), "reject") {
		t.Fatalf("missing-target truth not journaled: %q", body)
	}
}

// A failed draft persist leaves the cached proposal pending for retry.
func TestIntentSinkErrorRetriesWithoutModelCall(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "remind me to call the bank Friday"),
	})
	calls := 0
	o := Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			calls++
			return `{"summary":"s","facts":[],"intents":[{"text":"call the bank Friday","quote":"remind me to call the bank Friday"}]}`, nil
		},
		Now: dayNoon,
		IntentSink: func(ctx context.Context, p IntentProposal) error {
			return errors.New("assignments down")
		},
	}
	if _, err := Run(context.Background(), root, dayNoon, o); err == nil {
		t.Fatal("failed draft was marked complete")
	}
	if DayComplete(root, dayNoon.Format("2006-01-02")) {
		t.Fatal("failed draft completed night")
	}
	landed := 0
	o.IntentSink = func(context.Context, IntentProposal) error { landed++; return nil }
	if _, err := Run(context.Background(), root, dayNoon, o); err != nil || calls != 1 || landed != 1 {
		t.Fatalf("retry: model calls=%d drafts=%d err=%v", calls, landed, err)
	}
}

func TestBeforeCommitDenialLeavesVaultUntouched(t *testing.T) {
	root := fixture(t, []memory.Entry{eligibleEntry("turn-1", "the cedar contract ends Friday")})
	day := dayNoon.Format("2006-01-02")
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(context.Context, string, string) (string, error) {
			return `{"summary":"s","facts":[{"card":"notes/cedar","fact":"contract ends Friday","quote":"the cedar contract ends Friday"}]}`, nil
		},
		BeforeCommit: func() error { return errors.New("revoked") },
	})
	if err == nil {
		t.Fatal("revoked run committed")
	}
	if _, err := os.Stat(dayCachePath(root, day)); !os.IsNotExist(err) {
		t.Fatalf("checkpoint written after revoke: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "memory", "cards", "notes", "cedar.md")); !os.IsNotExist(err) {
		t.Fatalf("fact written after revoke: %v", err)
	}
}

func TestCorruptNightCheckpointFailsClosed(t *testing.T) {
	root := fixture(t, nil)
	day := dayNoon.Format("2006-01-02")
	path := dayCachePath(root, day)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if DayComplete(root, day) {
		t.Fatal("corrupt checkpoint completed")
	}
	if _, err := Run(context.Background(), root, dayNoon, Options{}); err == nil {
		t.Fatal("corrupt checkpoint restarted from empty")
	}
}

// R6.3: a struck entry never reaches the promotion prompt — soft forget
// applies to the nightly lane too, not just search/recall.
func TestStruckEntryNotPromoted(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", "the supplier is bergamot-free z3"),
		eligibleEntry("turn-2", "a second ordinary fact"),
	})
	if err := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Strike("turn-1"); err != nil {
		t.Fatal(err)
	}
	var prompt string
	if _, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			prompt = user
			return `{"summary":"s","facts":[]}`, nil
		},
		Now: dayNoon.Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "bergamot-free") {
		t.Fatalf("struck entry reached the promotion prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "second ordinary fact") {
		t.Fatalf("unstruck entry missing from prompt: %q", prompt)
	}
}

// SPEC-0085 R7.5: the prune lanes ask the rollup whether a day fully
// distilled — only a complete day-state lets bytes be cut; absent or
// partial states answer keep.
func TestDayComplete(t *testing.T) {
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	if DayComplete(root, "2026-04-19") {
		t.Fatal("no state answered complete")
	}
	cache := filepath.Join(root, "runtime", "memory", "night", "2026-04-19.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if DayComplete(root, "2026-04-19") {
		t.Fatal("partial state answered complete")
	}
	if err := os.WriteFile(cache, []byte(`{"entries":{},"steps":{"facts":true,"links":true,"truths":true,"intents":true,"core":true,"summary":true,"journal":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !DayComplete(root, "2026-04-19") {
		t.Fatal("complete state answered keep")
	}
}

// SPEC-0085 R7.5: when the day's eligible text exceeds the fold knob the
// same proposal call also asks for long_summary — the detailed digest
// lands in summaries/daily before any byte folding can cut the leaf.
func TestBigDayRequestsLongSummary(t *testing.T) {
	root := fixture(t, []memory.Entry{
		eligibleEntry("turn-1", strings.Repeat("a very busy day full of decisions ", 20)),
	})
	var prompt string
	res, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			prompt = user
			return `{"summary":"short","long_summary":"detailed digest covering every decision in depth","facts":[]}`, nil
		},
		Now:       dayNoon.Add(24 * time.Hour),
		FoldBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SummaryMD != "detailed digest covering every decision in depth" {
		t.Fatalf("SummaryMD = %q, want the written long summary", res.SummaryMD)
	}
	if !strings.Contains(prompt, "long_summary") {
		t.Fatalf("big-day prompt did not request long_summary")
	}
	sum, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "daily", dayNoon.Format("2006-01-02")+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sum), "detailed digest covering every decision") {
		t.Fatalf("long_summary was not written: %s", sum)
	}
}

// SPEC-0085 R7.5: a small day (or the knob off) never asks for the extra
// digest — one summary field is enough.
func TestSmallDayNoLongSummary(t *testing.T) {
	root := fixture(t, []memory.Entry{eligibleEntry("turn-1", "tiny day")})
	var prompt string
	_, err := Run(context.Background(), root, dayNoon, Options{
		Relay: func(ctx context.Context, system, user string) (string, error) {
			prompt = user
			return `{"summary":"short day","long_summary":"should not be used","facts":[]}`, nil
		},
		Now:       dayNoon.Add(24 * time.Hour),
		FoldBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "long_summary") {
		t.Fatal("small day prompt asked for long_summary")
	}
}
