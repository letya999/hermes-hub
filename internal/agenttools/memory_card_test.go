package agenttools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// writeRunScopeProv stamps a live run carrying audience + provenance — the
// gateway mints both at dispatch; the tool surface can never forge them.
func writeRunScopeProv(t *testing.T, root, audience, provenance string) {
	t.Helper()
	body := []byte(`{"runs":{"j-owner-1":{"audience":"` + audience + `","provenance":"` + provenance + `","expires":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}}}`)
	if err := os.MkdirAll(filepath.Join(root, "runtime", "run-scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "run-scope", "current"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// R6.1: the ops matrix — remember lands an inbox card, fact appends a dated
// evidence line to an existing card, truth replaces the Compiled Truth
// section through the governed writer.
func TestCardOpsMatrix(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}

	out, err := tls.memoryCard(Input{Op: "remember", Text: "owner prefers earl grey in the morning"})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if out["status"] != "ok" {
		t.Fatalf("remember status %v", out)
	}
	inbox := filepath.Join(root, "memory", "cards", "notes", "inbox")
	matches, _ := filepath.Glob(filepath.Join(inbox, "*.md"))
	if len(matches) != 1 {
		t.Fatalf("inbox cards %v want 1", matches)
	}
	body, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(body), "earl grey") || !strings.Contains(string(body), "type: note") {
		t.Fatalf("inbox card content missing note: %q", body)
	}

	out, err = tls.memoryCard(Input{Op: "fact", Card: "tea", Text: "supplier chosen", Evidence: "turn-1", Tags: []string{"tea", "suppliers"}})
	if err != nil {
		t.Fatalf("fact: %v", err)
	}
	if out["status"] != "ok" {
		t.Fatalf("fact status %v", out)
	}
	card, err := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	if err != nil {
		t.Fatalf("fact card missing: %v", err)
	}
	if !strings.Contains(string(card), "supplier chosen") {
		t.Fatalf("fact line absent: %q", card)
	}
	if !strings.Contains(string(card), "turn-1") {
		t.Fatalf("fact line carries no evidence ref: %q", card)
	}
	if !strings.Contains(string(card), "tags: [tea, suppliers]") {
		t.Fatalf("frontmatter dropped tags: %q", card)
	}

	out, err = tls.memoryCard(Input{Op: "truth", Card: "tea", Text: "earl grey supplier is signed"})
	if err != nil {
		t.Fatalf("truth: %v", err)
	}
	if out["status"] != "ok" {
		t.Fatalf("truth status %v", out)
	}
	card, _ = os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "tea.md"))
	if !strings.Contains(string(card), "## Compiled Truth") || !strings.Contains(string(card), "signed") {
		t.Fatalf("truth section absent: %q", card)
	}
}

// R6.6: a malformed call returns the corrected call shape with field-level
// errors — a result, not a transport failure. Identical invalid repeats hit
// the bounded-failure path instead of an endless retry loop.
func TestBadSchemaReturnsShape(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}

	out, err := tls.memoryCard(Input{Op: "fact", Card: "tea"})
	if err != nil {
		t.Fatalf("invalid call must be a result, not an error: %v", err)
	}
	if out["status"] != "invalid" {
		t.Fatalf("status %v want invalid", out)
	}
	errs, _ := out["errors"].([]cardFieldError)
	var fields []string
	for _, e := range errs {
		fields = append(fields, e.Field)
	}
	if len(fields) == 0 {
		t.Fatalf("no field errors: %v", out)
	}
	shape, _ := out["shape"].(map[string]any)
	if shape["op"] != "fact" {
		t.Fatalf("shape missing corrected call: %v", out["shape"])
	}

	// Repeated identical invalid calls escalate to a hard refusal.
	var lastErr error
	for i := 0; i < 3; i++ {
		_, lastErr = tls.memoryCard(Input{Op: "fact", Card: "tea"})
	}
	if lastErr == nil {
		t.Fatal("repeated invalid calls must hit the bounded-failure path")
	}
}

// R6.1: a remembered note is searchable the same day — the write changes the
// vault fingerprint so the next Ensure() reindexes it.
func TestRememberSearchableSameDay(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memoryCard(Input{Op: "remember", Text: "vervain tisane before bed"}); err != nil {
		t.Fatal(err)
	}
	out, err := tls.memorySearch(Input{Query: "vervain tisane"})
	if err != nil {
		t.Fatal(err)
	}
	hits, _ := out["hits"].([]map[string]any)
	if len(hits) == 0 {
		t.Fatalf("remembered card not searchable same day: %v", out)
	}
}

// R6.2: merge executes only inside a run whose triggering turn carries owner
// authorization — a hub-fired run (provenance=system) is denied even though
// the call is well-formed.
func TestMergeDeniedWithoutOwnerTurn(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "system")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memoryCard(Input{Op: "remember", Name: "a", Text: "source card"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.memoryCard(Input{Op: "remember", Name: "b", Text: "target card"}); err != nil {
		t.Fatal(err)
	}
	out, err := tls.memoryCard(Input{Op: "merge", Card: "a", To: "b"})
	if err != nil {
		t.Fatalf("denial must be a result: %v", err)
	}
	if out["status"] != "denied" {
		t.Fatalf("system run merged cards: %v", out)
	}
}

// R6.2: a tool argument claiming "owner confirmed" mints nothing — forget on
// a non-owner run is denied however the call argues.
func TestForgetDeniedArgClaim(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "system")
	tls := &Tools{VaultDir: root}
	out, err := tls.memoryCard(Input{Op: "forget", ID: "turn-1", Authorized: true})
	if err != nil {
		t.Fatalf("denial must be a result: %v", err)
	}
	if out["status"] != "denied" {
		t.Fatalf("arg-claimed authorization was honored: %v", out)
	}
}

// R6.2: a replayed old authorization is denied — an expired run-scope stamp
// is not a live owner turn.
func TestReplayedAuthDenied(t *testing.T) {
	root := memoryVaultFixture(t)
	stale := []byte(`{"runs":{"j-old":{"audience":"private","provenance":"owner","expires":"2020-01-01T00:00:00Z"}}}`)
	if err := os.MkdirAll(filepath.Join(root, "runtime", "run-scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "run-scope", "current"), stale, 0o644); err != nil {
		t.Fatal(err)
	}
	tls := &Tools{VaultDir: root}
	out, err := tls.memoryCard(Input{Op: "forget", ID: "turn-1"})
	if err != nil {
		t.Fatalf("denial must be a result: %v", err)
	}
	if out["status"] != "denied" {
		t.Fatalf("expired stamp authorized forget: %v", out)
	}
}

// R6.2/R6.4: a group run never writes private memory — group-stamped turns
// (even owner-provenance member text) are denied on every mutating op, and a
// call with no live stamp at all is denied too.
func TestCardGroupRunDenied(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "group", "owner")
	tls := &Tools{VaultDir: root}
	for _, in := range []Input{
		{Op: "remember", Text: "x"},
		{Op: "fact", Card: "tea", Text: "x", Evidence: "turn-1"},
		{Op: "merge", Card: "a", To: "b"},
		{Op: "forget", ID: "turn-1"},
	} {
		out, err := tls.memoryCard(in)
		if err != nil {
			t.Fatalf("denial must be a result: %v", err)
		}
		if out["status"] != "denied" {
			t.Fatalf("group run wrote private memory: op=%s out=%v", in.Op, out)
		}
	}
	// No stamp at all → same fail-closed answer.
	writeRunScopeProv(t, root, "group", "owner")
	if err := os.Remove(filepath.Join(root, "runtime", "run-scope", "current")); err != nil {
		t.Fatal(err)
	}
	out, err := tls.memoryCard(Input{Op: "remember", Text: "x"})
	if err != nil {
		t.Fatalf("denial must be a result: %v", err)
	}
	if out["status"] != "denied" {
		t.Fatalf("unstamped run wrote memory: %v", out)
	}
}

// R6.3: a forgotten entry is excluded from search immediately — the strike
// lands in the hub-owned sidecar and the index rebuild drops it.
func TestForgetExcludedEverywhere(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	// Sanity: the entry is searchable before the strike.
	out, err := tls.memorySearch(Input{Query: "earl grey"})
	if err != nil || len(out["hits"].([]map[string]any)) == 0 {
		t.Fatalf("fixture entry not searchable: %v %v", out, err)
	}
	out, err = tls.memoryCard(Input{Op: "forget", ID: "turn-1"})
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if out["status"] != "ok" {
		t.Fatalf("forget status %v", out)
	}
	out, err = tls.memorySearch(Input{Query: "earl grey"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := out["hits"].([]map[string]any); len(hits) != 0 {
		t.Fatalf("forgotten entry still searchable: %v", hits)
	}
}

// R6.3: the strike survives a restart — a fresh process's ForgetSet and a
// rebuilt index still exclude the entry.
func TestForgetSurvivesRestart(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memoryCard(Input{Op: "forget", ID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	if got := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Set(); !got["turn-1"] {
		t.Fatal("strike not durable")
	}
	// A new indexer (fresh process) on the same vault still excludes it.
	idx, _, err := memorysearch.NewIndexer(root).Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if hits := idx.Search("earl grey", memorysearch.Options{Audience: "private", Now: time.Now()}); len(hits) != 0 {
		t.Fatalf("restarted index still returns the struck entry: %v", hits)
	}
}

// R6.3: the struck entry is visible only through history:true — git history
// keeps the bytes, the lanes drop them.
func TestForgottenVisibleOnlyInHistory(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memoryCard(Input{Op: "forget", ID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	out, err := tls.memorySearch(Input{Query: "earl grey", History: true})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := out["hits"].([]map[string]any); len(hits) == 0 {
		t.Fatal("history:true lost the struck entry entirely")
	}
}

// R6.3 red-team: deliberately re-ingesting the same entry id leaves it inert
// — the strike set is separate from capture dedup.
func TestReingestionInert(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memoryCard(Input{Op: "forget", ID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	daily, _ := vault.OpenDaily(root, time.UTC)
	if err := daily.Append(memory.Entry{ID: "turn-1", Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner,
		Audience: memory.AudiencePrivate, Body: "we picked the earl grey supplier",
		ObservedAt: time.Date(2026, 4, 19, 15, 0, 0, 0, time.UTC)}, "telegram"); err != nil {
		t.Fatal(err)
	}
	out, err := tls.memorySearch(Input{Query: "earl grey"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := out["hits"].([]map[string]any); len(hits) != 0 {
		t.Fatalf("re-ingested struck entry came back: %v", hits)
	}
}

// R6.3: forgetting a card flips its frontmatter status to superseded through
// the governed writer — the reindexed doc drops out of the default lanes.
func TestForgetCardStruck(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memoryCard(Input{Op: "remember", Name: "vervain-card", Text: "vervain tisane recipe"}); err != nil {
		t.Fatal(err)
	}
	out, err := tls.memorySearch(Input{Query: "vervain"})
	if err != nil || len(out["hits"].([]map[string]any)) == 0 {
		t.Fatalf("card not searchable: %v %v", out, err)
	}
	out, err = tls.memoryCard(Input{Op: "forget", Card: "vervain-card"})
	if err != nil {
		t.Fatalf("forget card: %v", err)
	}
	if out["status"] != "ok" {
		t.Fatalf("forget status %v", out)
	}
	card, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "inbox", "vervain-card.md"))
	if !strings.Contains(string(card), "status: superseded") {
		t.Fatalf("card not struck: %q", card)
	}
	out, err = tls.memorySearch(Input{Query: "vervain"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := out["hits"].([]map[string]any); len(hits) != 0 {
		t.Fatalf("struck card still searchable: %v", hits)
	}
}

// R6.1: merge folds the source card's body into the target through the
// governed writer and strikes the source superseded — one audited op.
func TestMergeCards(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	for _, c := range [][2]string{{"tea-stock", "we stock earl grey"}, {"tea-blends", "breakfast blend notes"}} {
		if _, err := tls.memoryCard(Input{Op: "remember", Name: c[0], Text: c[1]}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := tls.memoryCard(Input{Op: "merge", Card: "tea-stock", To: "tea-blends"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if out["status"] != "ok" {
		t.Fatalf("merge status %v", out)
	}
	target, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "inbox", "tea-blends.md"))
	if !strings.Contains(string(target), "we stock earl grey") {
		t.Fatalf("source body not folded into target: %q", target)
	}
	source, _ := os.ReadFile(filepath.Join(root, "memory", "cards", "notes", "inbox", "tea-stock.md"))
	if !strings.Contains(string(source), "status: superseded") {
		t.Fatalf("source card not struck: %q", source)
	}
}

// R6.4: the vault file root gives the agent library/ and other plain paths
// but denies every governed zone — memory/, runtime/, dot dirs — on writes.
func TestZoneGuardFileTools(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.File("write", Input{Root: "vault", Path: "library/notes/x.txt", Text: "keep me"}); err != nil {
		t.Fatalf("library write denied: %v", err)
	}
	for _, p := range []string{
		"memory/daily/2026-04-20.md", "memory/cards/notes/x.md",
		"memory/summaries/daily/2026-04-19.md", "memory/CORE.md",
		"runtime/memory/prov.db", ".git/hooks/x", ".ok/config",
	} {
		if _, err := tls.File("write", Input{Root: "vault", Path: p, Text: "forge"}); err == nil {
			t.Fatalf("write into %s allowed", p)
		}
	}
}

// R6.4: the contract path is confined — traversal-style card names resolve
// inside the cards zone, never outside it.
func TestZoneGuardContractPath(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	out, err := tls.memoryCard(Input{Op: "fact", Card: "../escape", Text: "one line"})
	if err != nil {
		t.Fatal(err)
	}
	if got := out["path"]; got != "memory/cards/notes/escape.md" {
		t.Fatalf("fact escaped the cards zone: %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "escape.md")); !os.IsNotExist(err) {
		t.Fatal("traversal write landed outside the vault")
	}
}

// R6.4 red-team: case, fullwidth-unicode and dot-segment tricks on the
// zone names are all denied — the guard compares normalized segments.
func TestZoneNameUnicodeTricks(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	for _, p := range []string{
		"MEMORY/daily/x.md", "Memory/Daily/x.md", "ｍｅｍｏｒｙ/daily/x.md",
		"RUNTIME/x", "./memory/daily/x.md", "library/../memory/daily/x.md",
	} {
		if _, err := tls.File("write", Input{Root: "vault", Path: p, Text: "forge"}); err == nil {
			t.Fatalf("tricky path %q allowed", p)
		}
	}
}

// R6.4: CORE.md accepts no file-tool write even under an owner run — the
// bounded writer (night rollup) is its only mutation path.
func TestCoreWriterOnly(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.File("write", Input{Root: "vault", Path: "memory/CORE.md", Text: "forged directive"}); err == nil {
		t.Fatal("CORE.md writable through file tools")
	}
}

// R6.4 red-team: a symlink inside the vault pointing at a governed zone
// must not alias the write past the name-based guard.
func TestZoneGuardSymlink(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if err := os.MkdirAll(filepath.Join(root, "library"), 0o755); err != nil {
		t.Fatal(err)
	}
	target, link := filepath.Join(root, "memory"), filepath.Join(root, "library", "alias")
	if err := os.Symlink(target, link); err != nil {
		// Unprivileged Windows cannot mint symlinks but can mint dir junctions.
		if out, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); jerr != nil {
			t.Skipf("no link mechanism: %v / %v %s", err, jerr, out)
		}
	}
	if _, err := tls.File("write", Input{Root: "vault", Path: "library/alias/daily/x.md", Text: "forge"}); err == nil {
		t.Fatal("symlink write into memory/ allowed")
	}
}

// R6.5: every mutation logs the authorizing turn's SourceRef; denied owner
// ops land in the same journal with their attempted target.
func TestWriteAuditSourceRef(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memoryCard(Input{Op: "remember", Text: "audit this"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "runtime", "memory", "writes.jsonl"))
	if err != nil {
		t.Fatalf("no audit journal: %v", err)
	}
	if !strings.Contains(string(raw), `"op":"remember"`) ||
		!strings.Contains(string(raw), `"source_ref":"j-owner-1"`) {
		t.Fatalf("audit missing op/source ref: %s", raw)
	}
	// Denied attempts are audited too — a crafted merge without an owner turn.
	writeRunScopeProv(t, root, "private", "agent")
	if out, _ := tls.memoryCard(Input{Op: "forget", ID: "turn-1"}); out["status"] != "denied" {
		t.Fatalf("expected denial, got %v", out)
	}
	raw, _ = os.ReadFile(filepath.Join(root, "runtime", "memory", "writes.jsonl"))
	if !strings.Contains(string(raw), `"status":"denied"`) || !strings.Contains(string(raw), `"op":"forget"`) {
		t.Fatalf("denial not audited: %s", raw)
	}
}

// R6.5: contract writes commit under the agent's hermes- identity — the
// audit trail and git attribution agree on who wrote.
func TestCommitAttribution(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	if _, err := tls.memoryCard(Input{Op: "remember", Text: "attribute me"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "log", "-1", "--format=%an")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "hermes-agent" {
		t.Fatalf("last commit attributed to %q", out)
	}
}

// R6.1 contract: memory_card ships in the immutable 9.0.0 catalog under
// the memory.write capability and dispatches through Call like every tool.
func TestMemoryCardCatalog(t *testing.T) {
	def := HubToolsDefinition()
	if def.Version != "9.0.0" {
		t.Fatalf("catalog version = %s, want 9.0.0", def.Version)
	}
	var spec *struct {
		found, write bool
	}
	for _, ts := range def.Tools {
		if ts.Name == "memory_card" {
			spec = &struct{ found, write bool }{true, ts.CapabilityID == "memory.write"}
			if ts.Effect != api.WriteEffect {
				t.Fatalf("memory_card effect = %v, want write", ts.Effect)
			}
			var hasOp bool
			for _, a := range ts.Arguments {
				if a.Name == "op" && a.Required {
					hasOp = true
				}
				if a.Name == "authorized" {
					t.Fatal("an authorized argument must never exist")
				}
			}
			if !hasOp {
				t.Fatal("memory_card missing required op argument")
			}
		}
	}
	if spec == nil || !spec.write {
		t.Fatalf("memory_card missing or mis-capabilitied: %+v", spec)
	}
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "owner")
	tls := &Tools{VaultDir: root}
	out, err := tls.Call(context.Background(), "memory_card", Input{Op: "remember", Text: "via catalog"})
	if err != nil {
		t.Fatalf("Call dispatch: %v", err)
	}
	if res, _ := out.(map[string]any); res["status"] != "ok" {
		t.Fatalf("dispatched remember: %v", out)
	}
}

// R6.2 red-team: a prompt-injected merge/forget inside an untrusted turn
// (forwarded/external content) is denied — provenance is hub-stamped, so no
// text or argument inside the turn can mint authorization.
func TestUntrustedTurnCardDenied(t *testing.T) {
	root := memoryVaultFixture(t)
	writeRunScopeProv(t, root, "private", "untrusted")
	tls := &Tools{VaultDir: root}
	for _, op := range []string{"merge", "forget"} {
		out, err := tls.memoryCard(Input{Op: op, Card: "a", To: "b", ID: "turn-1"})
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if out["status"] != "denied" {
			t.Fatalf("untrusted-turn %s not denied: %v", op, out)
		}
	}
}
