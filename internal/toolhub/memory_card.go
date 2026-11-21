package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// memoryCard executes the cataloged memory_card inside ToolHub (#518): the
// sole governed writer for model-driven memory mutations. The verified
// envelope picks the vault through the server-rendered resolver — arguments
// and headers never choose a user, root or audience. Admission ran upstream:
// this handler holds no authority of its own.
func (g *Gateway) memoryCard(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation, effective api.EffectiveBinding, arguments map[string]any) (map[string]any, error) {
	var err error
	op := strings.TrimSpace(api.ArgString(arguments, "op"))
	requestID := cardRequestID(corr.JobID, op, arguments)
	var out map[string]any
	if err := g.withMemoryVault(ctx, auth, func(root string) error {
		// The write re-checks the live enrollment at the commit point — a
		// republished disable that lands between admission and this lock
		// still prevents the mutation.
		if err := memoryEnrollment(ctx, g.Spawn, auth.PrincipalID, auth.Environment); err != nil {
			out = map[string]any{"status": "denied", "op": op}
			return cardJournalAppend(root, op, "", cardSourceRef(corr), requestID, "denied", nil)
		}
		// The admitted authority is re-verified at the commit point too —
		// a right or binding revoked between admission and this lock still
		// prevents the mutation.
		if err := g.Store.ReverifyEffective(effective); err != nil {
			out = map[string]any{"status": "denied", "op": op}
			return cardJournalAppend(root, op, "", cardSourceRef(corr), requestID, "denied", nil)
		}
		if err := g.requireMemoryAdmission(ctx, auth, corr); err != nil {
			out = map[string]any{"status": "denied", "op": op}
			return cardJournalAppend(root, op, "", cardSourceRef(corr), requestID, "denied", nil)
		}
		if op == "remember" || op == "fact" {
			out, err = applyCardRequest(root, auth, corr, op, requestID, arguments)
			return err
		}
		if prior, ok := cardJournalResult(root, requestID); ok {
			out = prior
			return nil
		}
		var opErr error
		if op == "propose" {
			out, opErr = g.cardPropose(ctx, root, auth, corr, effective, requestID, arguments)
		} else {
			out, opErr = cardApply(root, op, arguments)
		}
		if opErr != nil {
			return opErr
		}
		status, _ := out["status"].(string)
		// A propose call succeeded when the proposal persisted — journal it
		// "ok" so a transport retry replays the same proposal id instead of
		// minting a second one.
		if status == "proposal" {
			status = "ok"
		}
		return cardJournalAppend(root, op, cardResultTarget(out), cardSourceRef(corr), requestID, status, out)
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// cardFieldError names one argument and the rule it broke — the corrected
// shape an invalid answer carries back to the model.
type cardFieldError struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
}

// cardInvalid is the honest invalid-status result: nothing was written and
// the model receives the field-level correction.
func cardInvalid(op string, errs []cardFieldError) map[string]any {
	list := make([]any, 0, len(errs))
	for _, e := range errs {
		list = append(list, map[string]any{"field": e.Field, "rule": e.Rule})
	}
	return map[string]any{"status": "invalid", "op": op, "errors": list}
}

// cardJournalResult returns the journaled result of an already-applied
// request_id — a retry of one admitted call converges on it instead of
// writing twice.
func cardJournalResult(root, requestID string) (map[string]any, bool) {
	rows, err := os.ReadFile(filepath.Join(root, "runtime", "memory", "writes.jsonl")) // #nosec G703 -- server-owned memory journal path
	if err != nil {
		return nil, false
	}
	for _, line := range strings.Split(string(rows), "\n") {
		var row struct {
			RequestID string         `json:"request_id"`
			Status    string         `json:"status"`
			Result    map[string]any `json:"result"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.RequestID != requestID || row.Status != "ok" || row.Result == nil {
			continue
		}
		return row.Result, true
	}
	return nil, false
}

// cardSourceRef binds every journaled mutation to the run that authorized
// it — the durable (job, run) pair, never a caller-supplied ref.
func cardSourceRef(corr api.CallCorrelation) string {
	if corr.HermesRunID != "" {
		return corr.JobID + "/" + corr.HermesRunID
	}
	return corr.JobID
}

// cardRequestID derives the retry key for one admitted call: the job pins
// the run, the op and canonical arguments pin the intent. A transport or
// model retry reissues identical arguments under the same job and converges
// on the journaled result instead of double-writing; a fresh ToolCallID
// (nested tool_call mints one per dispatch) never defeats the dedupe.
func cardRequestID(jobID, op string, arguments map[string]any) string {
	canonical, _ := json.Marshal(arguments)
	sum := sha256.Sum256([]byte(jobID + "\x00" + op + "\x00" + string(canonical)))
	return hex.EncodeToString(sum[:16])
}

// cardJournalAppend records one row in the per-vault write journal — the
// R6.5 audit of every mutation and denial, also the retry-safe request
// ledger. A journal failure is returned so callers cannot report an
// unrecorded mutation as a success.
func cardJournalAppend(root, op, target, sourceRef, requestID, status string, result map[string]any) error {
	entry := map[string]any{
		"ts": time.Now().UTC().Format(time.RFC3339), "op": op,
		"target": target, "source_ref": sourceRef, "request_id": requestID,
		"status": status,
	}
	if result != nil {
		entry["result"] = result
	}
	row, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := filepath.Join(root, "runtime", "memory", "writes.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { // #nosec G703 -- server-owned memory journal path
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G703 -- server-owned memory journal path
	if err != nil {
		return err
	}
	if _, err := f.Write(append(row, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// cardResultTarget picks the audited resource for a journal row from an
// applied op's result.
func cardResultTarget(out map[string]any) string {
	for _, k := range []string{"path", "id"} {
		if s, ok := out[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// cardApply runs one op's vault mutation at root — the governed writer's
// zone whitelist and hash check apply inside, never delegated to callers.
func cardApply(root, op string, arguments map[string]any) (map[string]any, error) {
	switch op {
	case "truth", "merge", "forget":
		// ADR-0095 D6: edits to existing memory mutate only through a
		// durable proposal consumed by a verified owner action — the model
		// cannot confirm its own change, so direct execution is invalid.
		return cardInvalid(op, []cardFieldError{{Field: "op", Rule: "existing-memory edits go through op=propose with inner=truth|merge|forget"}}), nil
	}
	return cardInvalid(op, []cardFieldError{{Field: "op", Rule: "one of remember|fact|propose"}}), nil
}

// cardFind resolves a bare or group-qualified card name to its real path
// below memory/cards/; "" when nothing matches.
func cardFind(root, name string) string {
	clean := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(name, "\\", "/")))
	clean = strings.TrimPrefix(clean, "memory/cards/")
	clean = strings.TrimSuffix(clean, ".md")
	var found string
	base := filepath.Join(root, "memory", "cards")
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error { // #nosec G703 -- walks the server-owned cards dir only
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

// cardRel pins a named card under notes/ — the name stays inside the
// governed subtree no matter the caller-supplied separators.
func cardRel(name string) string {
	return "memory/cards/notes/" + strings.TrimSuffix(path.Base(path.Clean(strings.ReplaceAll(name, "\\", "/"))), ".md") + ".md"
}

// cardFactPlan renders a dated, evidence-linked line without mutating the
// card. The request intent is persisted before this content reaches Writer.
func cardFactPlan(root string, arguments map[string]any) (cardPlannedWrite, map[string]any, error) {
	card := strings.TrimSpace(api.ArgString(arguments, "card"))
	text := strings.TrimSpace(api.ArgString(arguments, "text"))
	var errs []cardFieldError
	if card == "" {
		errs = append(errs, cardFieldError{Field: "card", Rule: "required"})
	}
	if text == "" {
		errs = append(errs, cardFieldError{Field: "text", Rule: "required"})
	}
	if len(errs) > 0 {
		return cardPlannedWrite{}, cardInvalid("fact", errs), nil
	}
	rel := cardRel(card)
	day := time.Now().Format("2006-01-02")
	line := "- " + day + ": " + oneLineText(text)
	if ev := oneLineText(api.ArgString(arguments, "evidence")); ev != "" {
		line += " · [evidence: " + ev + "]"
	}
	line += "\n"
	cur, baseSHA, err := vault.NewWriter(root).Read(rel)
	if err != nil {
		return cardPlannedWrite{}, nil, err
	}
	var content []byte
	if len(cur) == 0 {
		content = []byte(cardFrontmatter(day, api.ArgStrings(arguments, "tags")) + "\n## Facts\n\n" + line)
	} else {
		content = append(append([]byte{}, cur...), []byte(line)...)
	}
	return cardPlannedWrite{Path: rel, BaseSHA: baseSHA, Content: content}, map[string]any{"status": "ok", "op": "fact", "path": rel}, nil
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

func cardFrontmatter(dayKey string, tags []string) string {
	list := "[]"
	if len(tags) > 0 {
		list = "[" + strings.Join(tags, ", ") + "]"
	}
	return "---\ntype: note\nstatus: active\ntags: " + list + "\ncreated: " + dayKey + "\nupdated: " + dayKey + "\n---\n"
}

// cardRemember lands an inbox note card — the "запомни" lane.
func cardRememberPlan(root string, arguments map[string]any) (cardPlannedWrite, map[string]any, error) {
	text := strings.TrimSpace(api.ArgString(arguments, "text"))
	if text == "" {
		return cardPlannedWrite{}, cardInvalid("remember", []cardFieldError{{Field: "text", Rule: "required"}}), nil
	}
	day := time.Now().Format("2006-01-02")
	rel := "memory/cards/notes/inbox/" + slugOf(api.ArgString(arguments, "name"), text) + ".md"
	line := "- " + day + ": " + oneLineText(text) + "\n"
	cur, baseSHA, err := vault.NewWriter(root).Read(rel)
	if err != nil {
		return cardPlannedWrite{}, nil, err
	}
	var content []byte
	if len(cur) == 0 {
		content = []byte(cardFrontmatter(day, api.ArgStrings(arguments, "tags")) + "\n## Inbox\n\n" + line)
	} else {
		content = append(append([]byte{}, cur...), []byte(line)...)
	}
	return cardPlannedWrite{Path: rel, BaseSHA: baseSHA, Content: content}, map[string]any{"status": "ok", "op": "remember", "path": rel}, nil
}

func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
