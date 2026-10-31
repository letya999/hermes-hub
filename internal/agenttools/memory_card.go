package agenttools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// memoryCard is the M20.6 write contract into the memory vault (SPEC-0085
// R6, ADR-0086 D7): the only agent write path into the governed zones.
func (t *Tools) memoryCard(r Input) (map[string]any, error) {
	owner, live, ref := runCardAuth(t.VaultDir)
	if !live {
		out := map[string]any{"status": "denied", "reason": "no stamped private run authorizes vault writes"}
		cardAudit(t.VaultDir, r.Op, "", "", "denied")
		return out, nil
	}
	switch r.Op {
	case "remember", "fact", "truth", "merge", "forget":
	default:
		return nil, errors.New("unknown memory_card op")
	}
	if (r.Op == "merge" || r.Op == "forget") && !owner {
		out := map[string]any{"status": "denied", "reason": r.Op + " requires a live owner-provenance turn"}
		cardAudit(t.VaultDir, r.Op, cardTarget(r), ref, "denied")
		return out, nil
	}
	var out map[string]any
	var err error
	switch r.Op {
	case "remember":
		out, err = t.cardRemember(r)
	case "fact":
		out, err = t.cardFact(r)
	case "truth":
		out, err = t.cardTruth(r)
	case "merge":
		out, err = t.cardMerge(r)
	case "forget":
		out, err = t.cardForget(r)
	}
	if err == nil {
		cardAudit(t.VaultDir, r.Op, auditTarget(out), ref, "ok")
	}
	return out, err
}

// cardTarget picks the attempted resource for the audit journal.
func cardTarget(r Input) string {
	if r.Card != "" {
		return r.Card
	}
	return r.ID
}

// auditTarget extracts the written path/id from an op result.
func auditTarget(out map[string]any) string {
	for _, k := range []string{"path", "id"} {
		if s, ok := out[k].(string); ok && s != "" {
			return s
		}
	}
	if into, ok := out["into"].(string); ok {
		return fmt.Sprintf("%v → %s", out["from"], into)
	}
	return ""
}

// cardAudit appends one journal line to runtime/memory/writes.jsonl — R6.5:
// every mutation and every denial carries the authorizing turn's SourceRef.
func cardAudit(root, op, target, sourceRef, status string) {
	entry, err := json.Marshal(map[string]string{
		"ts": time.Now().UTC().Format(time.RFC3339), "op": op,
		"target": target, "source_ref": sourceRef, "status": status,
	})
	if err != nil {
		return
	}
	dir := filepath.Join(root, "runtime", "memory")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "writes.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(entry, '\n'))
}

// runCardAuth resolves the write authorization from the stamped live runs:
// every unexpired run must be private-audience (a concurrent group run
// confines writes exactly like reads) and at least one must exist; owner
// reports whether a live run carries owner provenance. Call arguments can
// never mint this authorization.
func runCardAuth(vaultDir string) (owner, live bool, ref string) {
	data, err := os.ReadFile(filepath.Join(vaultDir, "runtime", "run-scope", "current"))
	if err != nil {
		return false, false, ""
	}
	var stamp struct {
		Runs map[string]struct {
			Audience   string `json:"audience"`
			Provenance string `json:"provenance"`
			Expires    string `json:"expires"`
		} `json:"runs"`
	}
	if json.Unmarshal(data, &stamp) != nil {
		return false, false, ""
	}
	var jobs []string
	var ownerJob string
	for id, run := range stamp.Runs {
		if run.Expires != "" {
			if exp, err := time.Parse(time.RFC3339, run.Expires); err == nil && time.Now().After(exp) {
				continue
			}
		}
		if run.Audience != "private" {
			return false, false, ""
		}
		live = true
		jobs = append(jobs, id)
		if run.Provenance == "owner" && ownerJob == "" {
			ownerJob = id
		}
	}
	slices.Sort(jobs)
	if len(jobs) > 0 {
		ref = jobs[0]
	}
	if ownerJob != "" {
		owner = true
		ref = ownerJob
	}
	return owner, live, ref
}

type cardFieldError struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
}

// cardInvalid is the R6.6 corrected-shape answer: field-level errors plus
// the op's expected call shape so the model can fix the next call.
func cardInvalid(op string, errs []cardFieldError) map[string]any {
	shapes := map[string]map[string]any{
		"fact": {"op": "fact", "card": "card name or group/name", "text": "one line", "evidence": "captured entry id"},
	}
	return map[string]any{"status": "invalid", "errors": errs, "shape": shapes[op]}
}

// cardInvalidResult counts identical malformed call signatures — the third
// repeat of the same invalid call gets a hard refusal (R6.6 bounded-failure
// path) instead of an endless corrected-shape loop.
func (t *Tools) cardInvalidResult(sig string, out map[string]any) (map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cardInvalids == nil {
		t.cardInvalids = map[string]int{}
	}
	t.cardInvalids[sig]++
	if t.cardInvalids[sig] >= 3 {
		return nil, fmt.Errorf("memory_card: repeated invalid call — stop and use the corrected shape")
	}
	return out, nil
}

// cardRel maps a bare card name to its notes-group path below memory/cards/.
func cardRel(name string) string {
	return "memory/cards/notes/" + strings.TrimSuffix(path.Base(path.Clean(strings.ReplaceAll(name, "\\", "/"))), ".md") + ".md"
}

// cardBody returns the markdown below a card's --- frontmatter block.
func cardBody(doc string) string {
	if !strings.HasPrefix(doc, "---\n") {
		return doc
	}
	if i := strings.Index(doc[4:], "\n---"); i >= 0 {
		return strings.TrimLeft(doc[4+i+4:], "\n")
	}
	return doc
}

// cardFind resolves a bare or group-qualified card name to its real path
// below memory/cards/; "" when nothing matches.
func cardFind(root, name string) string {
	clean := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(name, "\\", "/")))
	clean = strings.TrimPrefix(clean, "memory/cards/")
	clean = strings.TrimSuffix(clean, ".md")
	var found string
	base := filepath.Join(root, "memory", "cards")
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		slash := filepath.ToSlash(rel)
		if slash == clean+".md" || d.Name() == clean+".md" {
			found = "memory/cards/" + slash
		}
		return nil
	})
	return found
}

// slugOf derives a filesystem-safe card slug from an explicit name or the
// text itself.
func slugOf(name, text string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	if base == "" {
		words := strings.Fields(strings.ToLower(text))
		if len(words) > 6 {
			words = words[:6]
		}
		base = strings.Join(words, " ")
	}
	var b strings.Builder
	lastDash := true
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case b.Len() > 0 && b.Len() < 40 && !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		sum := sha256.Sum256([]byte(text))
		slug = "note-" + hex.EncodeToString(sum[:4])
	}
	return slug
}

// shaOf returns the governed writer's expected content hash — "" when the
// file is absent or unreadable.
func shaOf(root, rel string) string {
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(cur)
	return hex.EncodeToString(sum[:])
}

func cardFrontmatter(dayKey string, tags []string) string {
	list := "[]"
	if len(tags) > 0 {
		list = "[" + strings.Join(tags, ", ") + "]"
	}
	return "---\ntype: note\nstatus: active\ntags: " + list + "\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n"
}

// cardApply runs one governed write of full new content for rel — the zone
// whitelist and hash check are the writer's, never this file's.
func cardApply(t *Tools, rel string, content []byte) error {
	_, err := vault.NewWriter(t.VaultDir).Apply(memory.WriteOp{
		Path:    rel,
		Content: content,
		WantSHA: shaOf(t.VaultDir, rel),
		Actor:   memory.ActorAgent,
	})
	return err
}

// cardRemember lands an inbox note card — the "запомни" lane.
func (t *Tools) cardRemember(r Input) (map[string]any, error) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return nil, errors.New("text required")
	}
	day := time.Now().Format("2006-01-02")
	rel := "memory/cards/notes/inbox/" + slugOf(r.Name, text) + ".md"
	line := "- " + day + ": " + oneLineText(text) + "\n"
	cur, err := os.ReadFile(filepath.Join(t.VaultDir, filepath.FromSlash(rel)))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var content []byte
	if len(cur) == 0 {
		content = []byte(cardFrontmatter(day, r.Tags) + "\n## Inbox\n\n" + line)
	} else {
		content = append(append([]byte{}, cur...), []byte(line)...)
	}
	if err := cardApply(t, rel, content); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "remember", "path": rel}, nil
}

// cardFact appends a dated, evidence-linked line to a named card — created
// under notes/ when absent.
func (t *Tools) cardFact(r Input) (map[string]any, error) {
	text := strings.TrimSpace(r.Text)
	if r.Card == "" || text == "" {
		var errs []cardFieldError
		if r.Card == "" {
			errs = append(errs, cardFieldError{Field: "card", Rule: "required"})
		}
		if text == "" {
			errs = append(errs, cardFieldError{Field: "text", Rule: "required"})
		}
		return t.cardInvalidResult(r.Op+"|"+r.Card+"|"+r.Text+"|"+r.Evidence, cardInvalid("fact", errs))
	}
	rel := cardRel(r.Card)
	day := time.Now().Format("2006-01-02")
	line := "- " + day + ": " + oneLineText(text)
	if r.Evidence != "" {
		line += " · [evidence: " + oneLineText(r.Evidence) + "]"
	}
	line += "\n"
	cur, err := os.ReadFile(filepath.Join(t.VaultDir, filepath.FromSlash(rel)))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var content []byte
	if len(cur) == 0 {
		content = []byte(cardFrontmatter(day, r.Tags) + "\n## Facts\n\n" + line)
	} else {
		content = append(append([]byte{}, cur...), []byte(line)...)
	}
	if err := cardApply(t, rel, content); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "fact", "path": rel}, nil
}

// cardTruth replaces a card's ## Compiled Truth body through the governed
// writer — the D5 hash-check makes a concurrent owner edit win.
func (t *Tools) cardTruth(r Input) (map[string]any, error) {
	text := strings.TrimSpace(r.Text)
	if r.Card == "" || text == "" {
		return nil, errors.New("card and text required")
	}
	rel := cardRel(r.Card)
	cur, err := os.ReadFile(filepath.Join(t.VaultDir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, errors.New("unknown card")
	}
	content := replaceSection(string(cur), "## Compiled Truth", text+"\n")
	if err := cardApply(t, rel, []byte(content)); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "truth", "path": rel}, nil
}

// replaceSection swaps the body under one ## heading (up to the next ##
// heading or EOF); the section is appended when absent.
func replaceSection(doc, heading, body string) string {
	i := strings.Index(doc, heading)
	if i < 0 {
		return strings.TrimRight(doc, "\n") + "\n\n" + heading + "\n\n" + body
	}
	rest := doc[i+len(heading):]
	end := len(rest)
	if j := strings.Index(rest, "\n## "); j >= 0 {
		end = j + 1
	}
	return doc[:i] + heading + "\n\n" + strings.TrimRight(body, "\n") + "\n" + rest[end:]
}

func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// cardMerge folds the source card's body into the target and strikes the
// source superseded — both writes go through the governed writer.
func (t *Tools) cardMerge(r Input) (map[string]any, error) {
	src, dst := cardFind(t.VaultDir, r.Card), cardFind(t.VaultDir, r.To)
	if src == "" || dst == "" || src == dst {
		return nil, errors.New("card and to must resolve to two different cards")
	}
	srcBytes, err := os.ReadFile(filepath.Join(t.VaultDir, filepath.FromSlash(src)))
	if err != nil {
		return nil, err
	}
	dstBytes, err := os.ReadFile(filepath.Join(t.VaultDir, filepath.FromSlash(dst)))
	if err != nil {
		return nil, err
	}
	body := cardBody(string(srcBytes))
	if body == "" {
		return nil, errors.New("source card carries no body to merge")
	}
	merged := strings.TrimRight(string(dstBytes), "\n") + "\n\n" + body
	if err := cardApply(t, dst, []byte(merged)); err != nil {
		return nil, err
	}
	struck := strings.Replace(string(srcBytes), "status: active", "status: superseded", 1)
	if err := cardApply(t, src, []byte(struck)); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "merge", "into": dst, "from": src}, nil
}

// cardForget is the soft-forget op (R6.3/D16): an entry id is struck in the
// hub-owned sidecar; a card's frontmatter flips to status: superseded
// through the governed writer. Both are excluded everywhere except
// history:true and git.
func (t *Tools) cardForget(r Input) (map[string]any, error) {
	if r.ID == "" && r.Card == "" {
		return nil, errors.New("id or card required")
	}
	if r.ID != "" {
		if err := memory.OpenForgetSet(filepath.Join(t.VaultDir, "runtime", "memory")).Strike(r.ID); err != nil {
			return nil, err
		}
		return map[string]any{"status": "ok", "op": "forget", "id": r.ID}, nil
	}
	rel := cardFind(t.VaultDir, r.Card)
	if rel == "" {
		return nil, errors.New("unknown card")
	}
	cur, err := os.ReadFile(filepath.Join(t.VaultDir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, errors.New("unknown card")
	}
	content := strings.Replace(string(cur), "status: active", "status: superseded", 1)
	if content == string(cur) {
		return nil, errors.New("card carries no active status to strike")
	}
	if err := cardApply(t, rel, []byte(content)); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "forget", "path": rel}, nil
}
