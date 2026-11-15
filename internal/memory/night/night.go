// Package night owns the per-space nightly memory rollup (SPEC-0085 R5,
// ADR-0086 D4/D14): deterministic Go does every read, validation, write and
// commit; the model only returns a JSON proposal through the injected relay.
package night

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// RelayFunc is the model seam — the space's model-relay behind a test
// fixture. It takes the system and user prompts and returns assistant text.
type RelayFunc func(ctx context.Context, system, user string) (string, error)

// Options binds one rollup run's inputs.
type Options struct {
	Relay      RelayFunc      // required — the space's model-relay (fixture in tests)
	Now        time.Time      // run instant (default: time.Now)
	TZ         *time.Location // owner timezone for day assignment (default: UTC)
	DryRun     bool           // evaluate and journal intent only — nothing writes
	MaxLossPct int            // bound on a truth rewrite's dropped-entry share (R5.4; 0→20)
	// IntentSink receives verified intent proposals for owner approval
	// (R5.7). The wiring binds it to the assignments draft path; intents
	// are never stored as facts and never auto-activated.
	IntentSink func(ctx context.Context, p IntentProposal) error
	// MaxCalls and MaxInputTokens bound one night's model spend (R5.8);
	// exceeding stops cleanly before any write. Zero means the spec
	// defaults (40 calls / 300k tokens).
	MaxCalls       int
	MaxInputTokens int
	// FoldBytes marks a day "big" for the R7.5 prune contract: eligible
	// text over this bound makes the proposal also request long_summary,
	// so the detailed digest lands in summaries/daily before the leaf's
	// audit bytes may be folded.
	FoldBytes int64
	// BeforeCommit rechecks the current owner's enrollment and right after
	// proposal generation and before any durable vault mutation.
	BeforeCommit func() error
}

// IntentProposal is one "remind me"-style line promoted to an owner
// approval item — quote-gated exactly like facts.
type IntentProposal struct {
	Text  string
	Quote string
}

// Result reports what the run did.
type Result struct {
	Day       string
	Facts     int
	Rejected  int
	Calls     int
	Digest    string
	Stopped   string // ""/"budget"/"halt"
	SummaryMD string // folded summary text actually written
}

// proposal is the lenient-parsed JSON the model returns for one day.
type proposal struct {
	Summary     string   `json:"summary"`
	LongSummary string   `json:"long_summary"`
	CardRefs    []string `json:"card_refs"`
	Facts       []struct {
		Card  string `json:"card"`
		Fact  string `json:"fact"`
		Quote string `json:"quote"`
	} `json:"facts"`
	Links   [][2]string     `json:"links"`
	Truths  []truthProposal `json:"truths"`
	Intents []struct {
		Text  string `json:"text"`
		Quote string `json:"quote"`
	} `json:"intents"`
	Core coreProposal `json:"core"`
}

// truthProposal rewrites one card wholesale; the bounded-loss gate (R5.4)
// decides whether it applies, falls back append-only, or is rejected.
type truthProposal struct {
	Card    string `json:"card"`
	Content string `json:"content"`
}

type coreProposal struct {
	Directives []struct {
		Key   string `json:"key"`
		Class string `json:"class"`
		Text  string `json:"text"`
		Quote string `json:"quote"`
	} `json:"directives"`
	Facts []struct {
		Text  string `json:"text"`
		Quote string `json:"quote"`
	} `json:"facts"`
}

// Run executes one night's rollup for the day containing `day` (owner tz).
func Run(ctx context.Context, root string, day time.Time, o Options) (Result, error) {
	var res Result
	tz := o.TZ
	if tz == nil {
		tz = time.UTC
	}
	dayKey := day.In(tz).Format("2006-01-02")
	res.Day = dayKey

	leaf, err := os.ReadFile(filepath.Join(root, "memory", "daily", dayKey+".md"))
	if err != nil && !os.IsNotExist(err) {
		return res, err
	}
	eligible, ownerOnly, blocks := eligibleText(root, string(leaf), dayKey, tz)

	// R5.8: a fully-stepped day state makes a rerun a no-op.
	st, err := loadDayState(root, dayKey)
	if err != nil {
		return res, err
	}
	if st.complete() {
		res.Calls, res.Digest = st.Calls, st.Digest
		return res, nil
	}
	res.Calls = st.Calls
	// An already-processed entry whose body changed since it was recorded
	// halts the day — the cached evidence no longer matches the leaf.
	for id, sha := range st.Entries {
		if body, ok := blocks[id]; !ok || sha256Hex([]byte(body)) != sha {
			res.Stopped = "halt"
			return res, nil
		}
	}
	// A day with no eligible entries completes its state without a model
	// call and without a vacuous summary — the fold renders it as the
	// "(no data)" marker (R5.9).
	if strings.TrimSpace(eligible) == "" {
		if o.DryRun {
			return res, nil
		}
		if o.BeforeCommit != nil {
			if err := o.BeforeCommit(); err != nil {
				return res, err
			}
		}
		for _, s := range allSteps {
			st.Steps[s] = true
		}
		return res, st.save(root, dayKey)
	}
	// R5.8: the spend caps stop the run before the model call and before
	// any write — a stopped night is a report, not a partial mutation.
	maxCalls := o.MaxCalls
	if maxCalls <= 0 {
		maxCalls = 40
	}
	maxTokens := o.MaxInputTokens
	if maxTokens <= 0 {
		maxTokens = 300000
	}
	if res.Calls >= maxCalls && len(st.Proposal) == 0 {
		res.Stopped = "budget"
		return res, nil
	}
	wantLong := o.FoldBytes > 0 && int64(len(eligible)) > o.FoldBytes
	if estTokens(buildPrompt(eligible, wantLong)) > maxTokens {
		res.Stopped = "budget"
		return res, nil
	}

	var p proposal
	if len(st.Proposal) > 0 {
		// Resume: the recorded proposal is reused — the model is not
		// re-called (R5.8).
		if err := json.Unmarshal(st.Proposal, &p); err != nil {
			return res, fmt.Errorf("night cached proposal JSON: %w", err)
		}
	} else {
		out, err := o.Relay(ctx, proposalSystem, buildPrompt(eligible, wantLong))
		res.Calls++
		if err != nil {
			// The call was spent even though it failed — record it so a
			// retry-resume counts it against the night's cap (R5.8).
			st.Calls = res.Calls
			if !o.DryRun {
				if o.BeforeCommit == nil || o.BeforeCommit() == nil {
					_ = st.save(root, dayKey)
				}
			}
			return res, fmt.Errorf("night proposal: %w", err)
		}
		if err := json.Unmarshal([]byte(proposalText(out)), &p); err != nil {
			return res, fmt.Errorf("night proposal JSON: %w", err)
		}
		st.Proposal = json.RawMessage(proposalText(out))
		for id, body := range blocks {
			st.Entries[id] = sha256Hex([]byte(body))
		}
	}
	// R7.5: an oversized day's detailed digest becomes the written summary —
	// the short text stays for the digest line.
	summaryMD := p.Summary
	if p.LongSummary != "" {
		summaryMD = p.LongSummary
	}
	res.SummaryMD = summaryMD

	// DryRun evaluates the proposal only — the vault, the journal, the
	// intent sink and the day cache all stay untouched.
	if o.DryRun {
		return res, nil
	}
	if o.BeforeCommit != nil {
		if err := o.BeforeCommit(); err != nil {
			return res, err
		}
	}
	if err := st.save(root, dayKey); err != nil {
		return res, err
	}

	// R8.4: sweep owner edits (the OK UI path) into vault history before any
	// run write — a rollup commit must never absorb owner bytes under a
	// system author. The sweep is idempotent: a clean tree commits nothing,
	// and DryRun has already returned so a dry night mutates nothing.
	if _, err := vault.CommitExternalEdits(root); err != nil {
		return res, err
	}
	w := vault.NewWriter(root)
	// A resumed run keeps the pre-crash journal lines and digest so the
	// day's REVIEW section and owner brief reflect every promotion the
	// day made, not only the resume's slice (R5.6/R5.8).
	j := journal{lines: append([]string{}, st.Journal...)}
	res.Digest = st.Digest
	persist := func() error {
		st.Journal = j.lines
		st.Digest = res.Digest
		st.Calls = res.Calls
		return st.save(root, dayKey)
	}
	guard := func() error {
		if o.BeforeCommit != nil {
			return o.BeforeCommit()
		}
		return nil
	}
	if !st.done("facts") {
		for _, f := range p.Facts {
			if err := guard(); err != nil {
				return res, err
			}
			// The quote must be verbatim eligible text — untrusted or
			// background content can neither reach the prompt nor verify.
			if f.Quote == "" || !strings.Contains(eligible, f.Quote) {
				res.Rejected++
				j.add("rejected fact %q on %s — quote not found in eligible entries", oneLine(f.Fact), f.Card)
				continue
			}
			if err := appendFact(w, root, f.Card, dayKey, f.Fact); err != nil {
				res.Rejected++
				j.add("rejected fact %q on %s — %v", oneLine(f.Fact), f.Card, err)
				continue
			}
			res.Facts++
			j.add("promoted fact on %s: %s", f.Card, oneLine(f.Fact))
			if res.Digest != "" {
				res.Digest += "\n"
			}
			res.Digest += "- " + oneLine(f.Fact)
		}
		st.Steps["facts"] = true
		if err := persist(); err != nil {
			return res, err
		}
	}
	if !st.done("links") {
		// R5.2(c): bilateral card links land only when both targets exist.
		for _, pair := range p.Links {
			if err := guard(); err != nil {
				return res, err
			}
			a, b := resolveCard(root, pair[0]), resolveCard(root, pair[1])
			if a == "" || b == "" {
				res.Rejected++
				j.add("rejected link %s <-> %s — target card missing", pair[0], pair[1])
				continue
			}
			if err := linkCards(w, root, a, b); err != nil {
				res.Rejected++
				j.add("rejected link %s <-> %s — %v", pair[0], pair[1], err)
			} else {
				j.add("linked %s <-> %s", pair[0], pair[1])
			}
		}
		st.Steps["links"] = true
		if err := persist(); err != nil {
			return res, err
		}
	}
	maxLoss := o.MaxLossPct
	if maxLoss <= 0 {
		maxLoss = 20
	}
	if !st.done("truths") {
		if err := guard(); err != nil {
			return res, err
		}
		res.Rejected += applyTruths(w, root, &j, p.Truths, maxLoss)
		st.Steps["truths"] = true
		if err := persist(); err != nil {
			return res, err
		}
	}
	if !st.done("intents") {
		if err := guard(); err != nil {
			return res, err
		}
		// R5.7: intents leave as owner-approval proposals through the
		// sink — never written to cards or CORE.
		if o.IntentSink != nil {
			for _, it := range p.Intents {
				if it.Quote == "" || !strings.Contains(eligible, it.Quote) {
					res.Rejected++
					j.add("rejected intent %q — quote not found in eligible entries", oneLine(it.Text))
					continue
				}
				if err := o.IntentSink(ctx, IntentProposal{Text: oneLine(it.Text), Quote: it.Quote}); err != nil {
					return res, err
				}
			}
		}
		st.Steps["intents"] = true
		if err := persist(); err != nil {
			return res, err
		}
	}
	if !st.done("core") {
		if err := guard(); err != nil {
			return res, err
		}
		// R5.2(d)/R5.5: bounded CORE edit — owner quotations only,
		// directives supersede in place, never-evict classes untouchable.
		rejected, err := applyCoreEdit(w, root, dayKey, p.Core, ownerOnly)
		res.Rejected += rejected
		if err != nil {
			return res, err
		}
		st.Steps["core"] = true
		if err := persist(); err != nil {
			return res, err
		}
	}
	if !st.done("summary") {
		if err := guard(); err != nil {
			return res, err
		}
		if err := writeDaySummary(w, dayKey, summaryMD, p.CardRefs); err != nil {
			return res, err
		}
		st.Steps["summary"] = true
		if err := persist(); err != nil {
			return res, err
		}
	}
	if !st.done("journal") {
		if err := guard(); err != nil {
			return res, err
		}
		// R5.6: the journal lands last — one dated, human-readable
		// section recording every promotion, skip and rejection.
		if err := j.write(w, root, dayKey); err != nil {
			return res, err
		}
		st.Steps["journal"] = true
		if err := persist(); err != nil {
			return res, err
		}
	}
	return res, nil
}

// proposalText unwraps a chat model's JSON proposal — relays commonly
// wrap the structured answer in ```json fences; anything else passes
// through unchanged so the caller's unmarshal reports the honest error.
func proposalText(out string) string {
	s := strings.TrimSpace(out)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

const proposalSystem = `You are the nightly memory rollup of a personal assistant.
You receive the day's eligible memory entries and return exactly one JSON object:
{"summary":"2-4 sentence day digest","facts":[{"card":"notes/<slug>","fact":"durable fact","quote":"verbatim owner/agent quote from the entries"}]}
Rules: facts promote only durable preferences, decisions and people — never small
talk. Every fact's quote must appear verbatim in the entries. Cards name a
memory/cards/<group>/<slug> target. Answer with JSON only.`

func buildPrompt(entries string, wantLong bool) string {
	p := "## Day entries\n\n" + entries + "\n## Task\nReturn the rollup JSON for this day."
	if wantLong {
		p += " The day was very active — also return \"long_summary\" with a detailed digest covering every decision and fact in depth."
	}
	return p
}

// eligibleText renders only promotion-eligible entry bodies for the day —
// untrusted, system and background kinds never reach the prompt, so a fact
// can never verify a quote against them either (SPEC-0085 R5.3). The second
// return is the owner-provenance slice: CORE edits verify against owner
// quotations only (R5.2(d)). The third maps entry id → block body for the
// day cache's changed-entry halt (R5.8).
func eligibleText(root, leaf, dayKey string, tz *time.Location) (string, string, map[string]string) {
	prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
	if err != nil {
		return "", "", nil
	}
	entries, err := prov.Entries()
	if err != nil {
		return "", "", nil
	}
	struck := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Set()
	var b, own strings.Builder
	blocks := map[string]string{}
	for _, e := range entries {
		if struck[e.ID] {
			continue
		}
		if e.ObservedAt.In(tz).Format("2006-01-02") != dayKey || !e.PromotionEligible() {
			continue
		}
		if body := leafBlock(leaf, e.ID); body != "" {
			blocks[e.ID] = body
			b.WriteString("### " + e.ObservedAt.In(tz).Format("15:04") + " " + string(e.Provenance) + "\n" + body + "\n")
			if e.Provenance == memory.ProvenanceOwner {
				own.WriteString(body + "\n")
			}
		}
	}
	return b.String(), own.String(), blocks
}

// leafBlock extracts one fenced entry body by its mem:id marker.
func leafBlock(leaf, id string) string {
	marker := "mem:id=" + id + " "
	i := strings.Index(leaf, marker)
	if i < 0 {
		return ""
	}
	rest := leaf[i:]
	if j := strings.Index(rest, "```text\n"); j >= 0 {
		rest = rest[j+len("```text\n"):]
	}
	if k := strings.Index(rest, "\n```"); k >= 0 {
		rest = rest[:k]
	}
	return strings.TrimSpace(rest)
}

// appendFact lands one dated, evidence-linked fact line on the named card —
// created under memory/cards/ as a note when absent. Every write goes
// through the governed writer: hash-checked, zone-confined, committed.
func appendFact(w *vault.Writer, root, card, dayKey, fact string) error {
	rel := "memory/cards/" + strings.TrimPrefix(filepath.ToSlash(filepath.Clean(card)), "memory/cards/")
	if !strings.HasSuffix(rel, ".md") {
		rel += ".md"
	}
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	line := "- " + dayKey + ": " + oneLine(fact) + " · [[daily/" + dayKey + "]]\n"
	if strings.Contains(string(cur), line) {
		return nil
	}
	var content []byte
	if len(cur) == 0 {
		content = []byte("---\ntype: note\nstatus: active\ntags: []\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n\n## Facts\n\n" + line)
	} else {
		// Facts are append-only (D5): the line joins the card's tail;
		// prior content — including owner edits — is byte-preserved.
		content = append(append([]byte{}, cur...), []byte(line)...)
	}
	_, err = w.Apply(memory.WriteOp{Path: rel, Content: content, WantSHA: wantOf(cur, err), Actor: memory.ActorSystem})
	return err
}

func writeDaySummary(w *vault.Writer, dayKey, summary string, refs []string) error {
	rel := "memory/summaries/daily/" + dayKey + ".md"
	content := "---\ntype: daily-summary\nstatus: active\ntags: []\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n\n" + strings.TrimSpace(summary) + "\n"
	_, err := w.Apply(memory.WriteOp{Path: rel, Content: []byte(content), Actor: memory.ActorSystem})
	return err
}

// cardGroups is the scaffold's card taxonomy (SPEC-0085 R1.1).
var cardGroups = []string{"contacts", "projects", "decisions", "ideas", "notes"}

// resolveCard maps a model-named card (slug or group/slug) to its real
// vault path below memory/cards/; "" when nothing matches.
func resolveCard(root, name string) string {
	clean := filepath.ToSlash(filepath.Clean(name))
	clean = strings.TrimPrefix(clean, "memory/cards/")
	if !strings.HasSuffix(clean, ".md") {
		clean += ".md"
	}
	if strings.Contains(clean, "/") {
		if _, err := os.Stat(filepath.Join(root, "memory", "cards", filepath.FromSlash(clean))); err == nil {
			return "memory/cards/" + clean
		}
		return ""
	}
	for _, group := range cardGroups {
		rel := "memory/cards/" + group + "/" + clean
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			return rel
		}
	}
	return ""
}

// linkCards appends the reciprocal [[link]] lines under each card's
// ## Links section — hash-checked through the governed writer.
func linkCards(w *vault.Writer, root, a, b string) error {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		if err := appendLink(w, root, pair[0], pair[1]); err != nil {
			return err
		}
	}
	return nil
}

func appendLink(w *vault.Writer, root, rel, target string) error {
	if !strings.HasSuffix(rel, ".md") {
		rel += ".md"
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	cur, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	stem := strings.TrimSuffix(filepath.Base(target), ".md")
	// Self-links are meaningless — a card never links to itself.
	if stem == strings.TrimSuffix(filepath.Base(rel), ".md") {
		return nil
	}
	content := append(append([]byte{}, cur...), []byte("\n## Links\n\n- [["+stem+"]]\n")...)
	if strings.Contains(string(cur), "- [["+stem+"]]") {
		return nil
	}
	_, err = w.Apply(memory.WriteOp{Path: rel, Content: content, WantSHA: wantOf(cur, err), Actor: memory.ActorSystem})
	return err
}

// applyCoreEdit lands the bounded CORE.md edit: each directive supersedes
// the dated line on the same key in place, and every proposed line must
// carry a verbatim owner quote (R5.2(d)).
func applyCoreEdit(w *vault.Writer, root, dayKey string, p coreProposal, ownerOnly string) (int, error) {
	if len(p.Directives) == 0 && len(p.Facts) == 0 {
		return 0, nil
	}
	abs := filepath.Join(root, "memory", "CORE.md")
	cur, err := os.ReadFile(abs)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	text := string(cur)
	rejected := 0
	for _, d := range p.Directives {
		if d.Quote == "" || !strings.Contains(ownerOnly, d.Quote) || d.Key == "" {
			rejected++
			continue
		}
		// R5.5: health/safety directives are never-evict — the pinned line
		// stands and the proposal is rejected.
		if old := directiveLine(text, d.Key); old != "" && directiveNeverEvict(old) {
			rejected++
			continue
		}
		line := "- " + dayKey + " [" + d.Class + "] " + d.Key + ": " + oneLine(d.Text)
		text = replaceDirectiveLine(text, d.Key, line)
	}
	for _, f := range p.Facts {
		if f.Quote == "" || !strings.Contains(ownerOnly, f.Quote) {
			rejected++
			continue
		}
		text = appendCoreFact(text, "- "+dayKey+": "+oneLine(f.Text))
	}
	if string(cur) == text {
		return rejected, nil // nothing landed — no write, no empty commit
	}
	_, err = w.Apply(memory.WriteOp{Path: "memory/CORE.md", Content: []byte(text), WantSHA: wantOf(cur, err), Actor: memory.ActorSystem})
	if err != nil {
		return rejected, err
	}
	return rejected, nil
}

// directiveLine returns the whole bullet line carrying `key:` under
// ## Directives, or "" — a line without a key is its own (never matched).
func directiveLine(core, key string) string {
	for _, line := range strings.Split(core, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		if k := directiveKey(trimmed); k != "" && k == key {
			return trimmed
		}
	}
	return ""
}

// directiveKey parses `- <date>? [<class>]? key: text` — the key is the
// token before the first colon once date/class decorations are dropped.
func directiveKey(line string) string {
	rest := strings.TrimPrefix(line, "- ")
	// skip a leading ISO date if present
	if len(rest) > 11 && rest[4] == '-' && rest[7] == '-' && rest[10] == ' ' {
		rest = rest[11:]
	}
	if i := strings.Index(rest, "]"); i >= 0 && strings.HasPrefix(rest, "[") {
		rest = rest[i+1:]
	}
	if i := strings.Index(rest, ":"); i > 0 {
		return strings.TrimSpace(rest[:i])
	}
	return ""
}

// replaceDirectiveLine swaps the line carrying key in place; a missing key
// appends the new line under ## Directives.
func replaceDirectiveLine(core, key, line string) string {
	old := directiveLine(core, key)
	if old != "" {
		return strings.Replace(core, old, line, 1)
	}
	if i := strings.Index(core, "## Directives"); i >= 0 {
		j := i + len("## Directives")
		return core[:j] + "\n" + line + core[j:]
	}
	return strings.TrimRight(core, "\n") + "\n\n## Directives\n" + line + "\n"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// wantOf is the WantSHA for a governed write: "" only when the file does
// not exist — an existing file's hash covers even empty content, matching
// the writer's stale-check.
func wantOf(cur []byte, readErr error) string {
	if readErr != nil {
		return ""
	}
	return sha256Hex(cur)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// neverEvictClasses pin directives that must never be superseded or
// removed (SPEC-0085 R5.5).
var neverEvictClasses = map[string]bool{"health": true, "safety": true}

func directiveNeverEvict(line string) bool {
	i := strings.Index(line, "[")
	j := strings.Index(line, "]")
	if i < 0 || j < 0 || j < i {
		return false
	}
	return neverEvictClasses[line[i+1:j]]
}

// appendCoreFact appends the dated line under ## Facts.
func appendCoreFact(core, line string) string {
	if strings.Contains(core, line) {
		return core
	}
	if i := strings.Index(core, "## Facts"); i >= 0 {
		j := i + len("## Facts")
		return core[:j] + "\n" + line + core[j:]
	}
	return strings.TrimRight(core, "\n") + "\n\n## Facts\n" + line + "\n"
}

// applyTruths runs the bounded-loss gate (R5.4): a wholesale rewrite that
// would drop more than MaxLossPct of the card's existing bullet entries is
// rejected — the prior body is byte-preserved and the proposal's new lines
// land append-only. Rewrites within the bound apply, with the replaced body
// preserved under ## History.
func applyTruths(w *vault.Writer, root string, j *journal, truths []truthProposal, maxLossPct int) int {
	rejected := 0
	for _, t := range truths {
		rel := resolveCard(root, t.Card)
		if rel == "" {
			rejected++
			j.add("rejected truth rewrite on %s — target card missing", t.Card)
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		cur, err := os.ReadFile(abs)
		if err != nil {
			rejected++
			j.add("rejected truth rewrite on %s — %v", t.Card, err)
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(string(cur)), strings.TrimSpace(t.Content)) {
			continue
		}
		oldLines := bulletLines(string(cur))
		var dropped []string
		for _, l := range oldLines {
			if !strings.Contains(t.Content, l) {
				dropped = append(dropped, l)
			}
		}
		var content []byte
		if len(oldLines) > 0 && len(dropped)*100 > maxLossPct*len(oldLines) {
			rejected++
			j.add("rejected truth rewrite on %s — would drop %d/%d entries (loss cap %d%%); prior body kept, new lines appended",
				t.Card, len(dropped), len(oldLines), maxLossPct)
			content = []byte(string(cur))
			for _, l := range bulletLines(t.Content) {
				if !strings.Contains(string(cur), l) {
					content = append(content, []byte(l+"\n")...)
				}
			}
		} else {
			content = []byte(moveToHistory(string(cur), t.Content))
		}
		if string(cur) == string(content) {
			continue
		}
		if _, err := w.Apply(memory.WriteOp{Path: rel, Content: content, WantSHA: wantOf(cur, err), Actor: memory.ActorSystem}); err != nil {
			rejected++
		}
	}
	return rejected
}

// bulletLines lists the card's "- "-prefixed entry lines.
func bulletLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "- ") {
			out = append(out, l)
		}
	}
	return out
}

// moveToHistory records the replaced body under the card's ## History
// section (D5) before the new content takes over.
func moveToHistory(old, new string) string {
	entry := "\n### replaced " + time.Now().UTC().Format("2006-01-02") + "\n\n" + strings.TrimSpace(old) + "\n"
	if i := strings.Index(new, "## History"); i >= 0 {
		j := i + len("## History")
		return new[:j] + entry + new[j:]
	}
	return strings.TrimRight(new, "\n") + "\n\n## History\n" + entry
}

// journal accumulates one run's decisions (R5.6) and appends them as a
// dated, human-readable section to memory/REVIEW.md through the governed
// writer — the journal itself is vault content, searchable and committed.
type journal struct{ lines []string }

func (j *journal) add(format string, args ...any) {
	j.lines = append(j.lines, fmt.Sprintf(format, args...))
}

func (j *journal) write(w *vault.Writer, root, dayKey string) error {
	rel := "memory/REVIEW.md"
	abs := filepath.Join(root, filepath.FromSlash(rel))
	cur, err := os.ReadFile(abs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// A crash between the write and its checkpoint leaves the section on
	// disk; the resume must not append a second one for the same day.
	if strings.Contains(string(cur), "## "+dayKey) {
		return nil
	}
	var b strings.Builder
	b.Write(cur)
	if len(cur) == 0 {
		b.WriteString("# REVIEW — nightly rollup journal\n")
	}
	fmt.Fprintf(&b, "\n## %s\n", dayKey)
	if len(j.lines) == 0 {
		b.WriteString("\n- no promotable proposals\n")
	}
	for _, l := range j.lines {
		b.WriteString("\n- " + l)
	}
	b.WriteString("\n")
	_, err = w.Apply(memory.WriteOp{Path: rel, Content: []byte(b.String()), WantSHA: wantOf(cur, err), Actor: memory.ActorSystem})
	return err
}

// dayState is the idempotent per-day cache under runtime/memory/night/
// (R5.8): entry hashes detect changed already-processed content, and the
// step flags resume a crashed run without re-calling the model. Journal
// and Digest persist the run's decisions so a resume still journals and
// reports everything the day promoted.
type dayState struct {
	Entries  map[string]string `json:"entries"`
	Proposal json.RawMessage   `json:"proposal,omitempty"`
	Calls    int               `json:"calls,omitempty"`
	Steps    map[string]bool   `json:"steps"`
	Journal  []string          `json:"journal,omitempty"`
	Digest   string            `json:"digest,omitempty"`
}

func dayCachePath(root, dayKey string) string {
	return filepath.Join(root, "runtime", "memory", "night", dayKey+".json")
}

func loadDayState(root, dayKey string) (*dayState, error) {
	b, err := os.ReadFile(dayCachePath(root, dayKey))
	if os.IsNotExist(err) {
		return &dayState{Entries: map[string]string{}, Steps: map[string]bool{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var st dayState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("night checkpoint unreadable: %w", err)
	}
	if st.Steps == nil {
		st.Steps = map[string]bool{}
	}
	if st.Entries == nil {
		st.Entries = map[string]string{}
	}
	return &st, nil
}

func (st *dayState) save(root, dayKey string) error {
	p := dayCachePath(root, dayKey)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".night-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

func (st *dayState) done(step string) bool { return st.Steps[step] }

// allSteps is every ordered pipeline stage; a fully-checked state is a
// completed day — reruns short-circuit before the model call.
var allSteps = []string{"facts", "links", "truths", "intents", "core", "summary", "journal"}

func (st *dayState) complete() bool {
	for _, s := range allSteps {
		if !st.Steps[s] {
			return false
		}
	}
	return true
}

// DayComplete reports whether the rollup fully processed dayKey (R7.5):
// the prune lanes may only cut bytes whose distillation already landed —
// an absent or partial state is a hard "keep".
func DayComplete(root, dayKey string) bool {
	st, err := loadDayState(root, dayKey)
	return err == nil && st.complete()
}

// estTokens is the conservative ~4 chars-per-token input estimate the
// R5.8 budget gates on — never a provider claim, always an upper bound.
func estTokens(s string) int { return len(s) / 4 }

// FoldWeek rolls the seven day states of the ISO week containing day
// into one weekly summary (R5.9). Every one of the seven days must have
// a terminal day state — a day never processed blocks the fold; a
// processed no-data day folds as an explicit marker.
func FoldWeek(ctx context.Context, root string, day time.Time, o Options) (Result, error) {
	var res Result
	tz := o.TZ
	if tz == nil {
		tz = time.UTC
	}
	monday := day.In(tz)
	for monday.Weekday() != time.Monday {
		monday = monday.AddDate(0, 0, -1)
	}
	year, week := monday.ISOWeek()
	rel := fmt.Sprintf("memory/summaries/weekly/%04d-W%02d.md", year, week)
	// A written fold is immutable — the day states under it can never be
	// reprocessed, and an owner edit of the summary is never clobbered.
	if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
		return res, nil
	}
	for i := 0; i < 7; i++ {
		d := monday.AddDate(0, 0, i).Format("2006-01-02")
		st, err := loadDayState(root, d)
		if err != nil {
			return res, err
		}
		if !st.complete() {
			res.Stopped = "incomplete"
			return res, nil
		}
	}
	var days strings.Builder
	for i := 0; i < 7; i++ {
		d := monday.AddDate(0, 0, i).Format("2006-01-02")
		body, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "daily", d+".md"))
		text := "(no data)"
		if err == nil {
			text = strings.TrimSpace(string(body))
		}
		fmt.Fprintf(&days, "### %s\n%s\n", d, text)
	}
	out, err := o.Relay(ctx, proposalSystem, "## Week days\n\n"+days.String()+"\n## Task\nReturn the weekly rollup JSON.")
	res.Calls++
	if err != nil {
		return res, fmt.Errorf("night weekly proposal: %w", err)
	}
	var p proposal
	if err := json.Unmarshal([]byte(proposalText(out)), &p); err != nil {
		return res, fmt.Errorf("night weekly proposal JSON: %w", err)
	}
	content := "---\ntype: weekly-summary\nstatus: active\ntags: []\ncreated: " + monday.Format("2006-01-02") + "\nupdated: " + monday.Format("2006-01-02") + "\n---\n\n" + strings.TrimSpace(p.Summary) + "\n"
	if o.BeforeCommit != nil {
		if err := o.BeforeCommit(); err != nil {
			return res, err
		}
	}
	w := vault.NewWriter(root)
	_, err = w.Apply(memory.WriteOp{Path: rel, Content: []byte(content), Actor: memory.ActorSystem})
	return res, err
}

// FoldYear rolls the year containing day into one yearly summary
// (R5.9): all twelve monthly summaries must exist — a missing month
// blocks the fold until its own fold chain completes it.
func FoldYear(ctx context.Context, root string, day time.Time, o Options) (Result, error) {
	var res Result
	tz := o.TZ
	if tz == nil {
		tz = time.UTC
	}
	year := day.In(tz).Year()
	rel := fmt.Sprintf("memory/summaries/yearly/%04d.md", year)
	if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
		return res, nil
	}
	var material strings.Builder
	for m := 1; m <= 12; m++ {
		key := fmt.Sprintf("%04d-%02d", year, m)
		body, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "monthly", key+".md"))
		if os.IsNotExist(err) {
			res.Stopped = "incomplete"
			return res, nil
		}
		if err != nil {
			return res, err
		}
		fmt.Fprintf(&material, "### %s\n%s\n", key, strings.TrimSpace(string(body)))
	}
	out, err := o.Relay(ctx, proposalSystem, "## Year material\n\n"+material.String()+"\n## Task\nReturn the yearly rollup JSON.")
	res.Calls++
	if err != nil {
		return res, fmt.Errorf("night yearly proposal: %w", err)
	}
	var p proposal
	if err := json.Unmarshal([]byte(proposalText(out)), &p); err != nil {
		return res, fmt.Errorf("night yearly proposal JSON: %w", err)
	}
	first := time.Date(year, 1, 1, 0, 0, 0, 0, tz)
	content := "---\ntype: yearly-summary\nstatus: active\ntags: []\ncreated: " + first.Format("2006-01-02") + "\nupdated: " + first.Format("2006-01-02") + "\n---\n\n" + strings.TrimSpace(p.Summary) + "\n"
	if o.BeforeCommit != nil {
		if err := o.BeforeCommit(); err != nil {
			return res, err
		}
	}
	w := vault.NewWriter(root)
	_, err = w.Apply(memory.WriteOp{Path: rel, Content: []byte(content), Actor: memory.ActorSystem})
	return res, err
}

// FoldMonth rolls the month containing day into one monthly summary
// (R5.9): every in-month day must have a terminal day state before the
// fold runs at all.
func FoldMonth(ctx context.Context, root string, day time.Time, o Options) (Result, error) {
	var res Result
	tz := o.TZ
	if tz == nil {
		tz = time.UTC
	}
	local := day.In(tz)
	first := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, tz)
	last := first.AddDate(0, 1, -1)
	rel := fmt.Sprintf("memory/summaries/monthly/%s.md", first.Format("2006-01"))
	if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
		return res, nil
	}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		st, err := loadDayState(root, d.Format("2006-01-02"))
		if err != nil {
			return res, err
		}
		if !st.complete() {
			res.Stopped = "incomplete"
			return res, nil
		}
	}
	var material strings.Builder
	mondayOf := func(d time.Time) time.Time {
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, -1)
		}
		return d
	}
	// Full in-month weeks — Monday and Sunday both inside the month —
	// contribute their weekly summaries; edge days contribute dailies.
	weeksDone := map[string]bool{}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		mon := mondayOf(d)
		if mon.Before(first) || mon.AddDate(0, 0, 6).After(last) {
			key := d.Format("2006-01-02")
			body, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "daily", key+".md"))
			text := "(no data)"
			if err == nil {
				text = strings.TrimSpace(string(body))
			}
			fmt.Fprintf(&material, "### %s\n%s\n", key, text)
			continue
		}
		y, w := mon.ISOWeek()
		wkey := fmt.Sprintf("%04d-W%02d", y, w)
		if weeksDone[wkey] {
			continue
		}
		weeksDone[wkey] = true
		body, err := os.ReadFile(filepath.Join(root, "memory", "summaries", "weekly", wkey+".md"))
		text := "(no data)"
		if err == nil {
			text = strings.TrimSpace(string(body))
		}
		fmt.Fprintf(&material, "### %s\n%s\n", wkey, text)
	}
	out, err := o.Relay(ctx, proposalSystem, "## Month material\n\n"+material.String()+"\n## Task\nReturn the monthly rollup JSON.")
	res.Calls++
	if err != nil {
		return res, fmt.Errorf("night monthly proposal: %w", err)
	}
	var p proposal
	if err := json.Unmarshal([]byte(proposalText(out)), &p); err != nil {
		return res, fmt.Errorf("night monthly proposal JSON: %w", err)
	}
	content := "---\ntype: monthly-summary\nstatus: active\ntags: []\ncreated: " + first.Format("2006-01-02") + "\nupdated: " + first.Format("2006-01-02") + "\n---\n\n" + strings.TrimSpace(p.Summary) + "\n"
	if o.BeforeCommit != nil {
		if err := o.BeforeCommit(); err != nil {
			return res, err
		}
	}
	w := vault.NewWriter(root)
	_, err = w.Apply(memory.WriteOp{Path: rel, Content: []byte(content), Actor: memory.ActorSystem})
	return res, err
}
