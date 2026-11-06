package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// memoryCard executes the cataloged memory_card inside ToolHub (#518): the
// sole governed writer for model-driven memory mutations. The verified
// envelope picks the vault through the server-rendered resolver — arguments
// and headers never choose a user, root or audience. Admission ran upstream:
// this handler holds no authority of its own.
func (g *Gateway) memoryCard(ctx context.Context, auth identity.Envelope, corr api.CallCorrelation, effective api.EffectiveBinding, arguments map[string]any) (map[string]any, error) {
	u, err := g.Vaults.Resolve(auth.PrincipalID, auth.Environment)
	if err != nil {
		return nil, err
	}
	op := strings.TrimSpace(api.ArgString(arguments, "op"))
	requestID := cardRequestID(corr.JobID, op, arguments)
	var out map[string]any
	if err := u.Locked(func(root string) error {
		// The write re-checks the live enrollment at the commit point — a
		// republished disable that lands between admission and this lock
		// still prevents the mutation.
		if err := memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment); err != nil {
			out = map[string]any{"status": "denied", "op": op}
			cardJournalAppend(root, op, "", cardSourceRef(corr), requestID, "denied", nil)
			return nil
		}
		// The admitted authority is re-verified at the commit point too —
		// a right or binding revoked between admission and this lock still
		// prevents the mutation.
		if err := g.Store.ReverifyEffective(effective); err != nil {
			out = map[string]any{"status": "denied", "op": op}
			cardJournalAppend(root, op, "", cardSourceRef(corr), requestID, "denied", nil)
			return nil
		}
		if prior, ok := cardJournalResult(root, requestID); ok {
			out = prior
			return nil
		}
		var opErr error
		out, opErr = cardApply(root, op, arguments)
		if opErr != nil {
			return opErr
		}
		status, _ := out["status"].(string)
		cardJournalAppend(root, op, cardResultTarget(out), cardSourceRef(corr), requestID, status, out)
		return nil
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
	rows, err := os.ReadFile(filepath.Join(root, "runtime", "memory", "writes.jsonl"))
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
// ledger. A journal failure never rewrites the honest answer of an applied
// write; it only loses that write's audit row.
func cardJournalAppend(root, op, target, sourceRef, requestID, status string, result map[string]any) {
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
		return
	}
	path := filepath.Join(root, "runtime", "memory", "writes.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(row, '\n'))
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
	case "remember":
		return cardRemember(root, arguments)
	case "fact":
		return cardFact(root, arguments)
	case "truth":
		return cardTruth(root, arguments)
	case "merge":
		return cardMerge(root, arguments)
	case "forget":
		return cardForget(root, arguments)
	}
	return cardInvalid(op, []cardFieldError{{Field: "op", Rule: "one of remember|fact|truth|merge|forget"}}), nil
}

// cardForget is the soft-forget op (R6.3/D16): an entry id is struck in the
// hub-owned sidecar; a card's frontmatter flips to status: superseded
// through the governed writer. Both are excluded everywhere except
// history:true and git.
func cardForget(root string, arguments map[string]any) (map[string]any, error) {
	id := strings.TrimSpace(api.ArgString(arguments, "id"))
	card := strings.TrimSpace(api.ArgString(arguments, "card"))
	if id == "" && card == "" {
		return nil, errors.New("id or card required")
	}
	if id != "" {
		if err := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Strike(id); err != nil {
			return nil, err
		}
		return map[string]any{"status": "ok", "op": "forget", "id": id}, nil
	}
	rel := cardFind(root, card)
	if rel == "" {
		return nil, errors.New("unknown card")
	}
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, errors.New("unknown card")
	}
	content := strings.Replace(string(cur), "status: active", "status: superseded", 1)
	if content == string(cur) {
		return nil, errors.New("card carries no active status to strike")
	}
	if err := cardWrite(root, rel, []byte(content)); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "forget", "path": rel}, nil
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

// cardMerge folds the source card's body into the target and strikes the
// source superseded — both writes go through the governed writer.
func cardMerge(root string, arguments map[string]any) (map[string]any, error) {
	src, dst := cardFind(root, api.ArgString(arguments, "card")), cardFind(root, api.ArgString(arguments, "to"))
	if src == "" || dst == "" || src == dst {
		return nil, errors.New("card and to must resolve to two different cards")
	}
	srcBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(src)))
	if err != nil {
		return nil, err
	}
	dstBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dst)))
	if err != nil {
		return nil, err
	}
	body := cardBody(string(srcBytes))
	if body == "" {
		return nil, errors.New("source card carries no body to merge")
	}
	merged := strings.TrimRight(string(dstBytes), "\n") + "\n\n" + body
	if err := cardWrite(root, dst, []byte(merged)); err != nil {
		return nil, err
	}
	struck := strings.Replace(string(srcBytes), "status: active", "status: superseded", 1)
	if err := cardWrite(root, src, []byte(struck)); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "merge", "into": dst, "from": src}, nil
}

// cardRel pins a named card under notes/ — the name stays inside the
// governed subtree no matter the caller-supplied separators.
func cardRel(name string) string {
	return "memory/cards/notes/" + strings.TrimSuffix(path.Base(path.Clean(strings.ReplaceAll(name, "\\", "/"))), ".md") + ".md"
}

// cardFact appends a dated, evidence-linked line to a named card — created
// under notes/ when absent.
func cardFact(root string, arguments map[string]any) (map[string]any, error) {
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
		return cardInvalid("fact", errs), nil
	}
	rel := cardRel(card)
	day := time.Now().Format("2006-01-02")
	line := "- " + day + ": " + oneLineText(text)
	if ev := oneLineText(api.ArgString(arguments, "evidence")); ev != "" {
		line += " · [evidence: " + ev + "]"
	}
	line += "\n"
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var content []byte
	if len(cur) == 0 {
		content = []byte(cardFrontmatter(day, api.ArgStrings(arguments, "tags")) + "\n## Facts\n\n" + line)
	} else {
		content = append(append([]byte{}, cur...), []byte(line)...)
	}
	if err := cardWrite(root, rel, content); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "fact", "path": rel}, nil
}

// cardTruth replaces a card's ## Compiled Truth body through the governed
// writer — the D5 hash-check makes a concurrent owner edit win.
func cardTruth(root string, arguments map[string]any) (map[string]any, error) {
	card := strings.TrimSpace(api.ArgString(arguments, "card"))
	text := strings.TrimSpace(api.ArgString(arguments, "text"))
	if card == "" || text == "" {
		return nil, errors.New("card and text required")
	}
	rel := cardRel(card)
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, errors.New("unknown card")
	}
	content := replaceSection(string(cur), "## Compiled Truth", text+"\n")
	if err := cardWrite(root, rel, []byte(content)); err != nil {
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

// cardWrite runs one governed write of full new content for rel — the zone
// whitelist and hash check are the writer's, never this file's.
func cardWrite(root, rel string, content []byte) error {
	_, err := vault.NewWriter(root).Apply(memory.WriteOp{
		Path:    rel,
		Content: content,
		WantSHA: shaOf(root, rel),
		Actor:   memory.ActorAgent,
	})
	return err
}

// cardRemember lands an inbox note card — the "запомни" lane.
func cardRemember(root string, arguments map[string]any) (map[string]any, error) {
	text := strings.TrimSpace(api.ArgString(arguments, "text"))
	if text == "" {
		return cardInvalid("remember", []cardFieldError{{Field: "text", Rule: "required"}}), nil
	}
	day := time.Now().Format("2006-01-02")
	rel := "memory/cards/notes/inbox/" + slugOf(api.ArgString(arguments, "name"), text) + ".md"
	line := "- " + day + ": " + oneLineText(text) + "\n"
	cur, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var content []byte
	if len(cur) == 0 {
		content = []byte(cardFrontmatter(day, api.ArgStrings(arguments, "tags")) + "\n## Inbox\n\n" + line)
	} else {
		content = append(append([]byte{}, cur...), []byte(line)...)
	}
	if err := cardWrite(root, rel, content); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "op": "remember", "path": rel}, nil
}

func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
