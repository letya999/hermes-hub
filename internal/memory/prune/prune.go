// Package prune owns the R7.5 leaf lifecycle lanes (ADR-0086 D20): FOLD
// stubs an oversized completed day's audit-kind block bodies in place, and
// EXPIRE removes leaves past retention once their distillation products
// provably exist. Every lane is fail-closed on the day's rollup state —
// bytes are only cut after the night verified them.
package prune

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/vault"
)

// Options binds the two prune knobs — both default off.
type Options struct {
	RetentionDays int            // expire leaves older than this many days; 0 disables
	FoldBytes     int64          // fold a completed oversized leaf; 0 disables
	Now           time.Time      // run instant (default: time.Now)
	TZ            *time.Location // owner timezone for day boundaries (default: UTC)
}

// FoldResult reports one folded leaf.
type FoldResult struct {
	Day    string `json:"day"`
	Before int    `json:"before"`
	After  int    `json:"after"`
	Stubs  int    `json:"stubs"`
}

// KeptDay records a retention-age leaf kept because its distillation could
// not be verified — reported, never deleted.
type KeptDay struct {
	Day    string `json:"day"`
	Reason string `json:"reason"`
}

// Report lists what the run found and did.
type Report struct {
	Folded  []FoldResult `json:"folded,omitempty"`
	Expired []string     `json:"expired,omitempty"`
	Kept    []KeptDay    `json:"kept,omitempty"`
}

var memID = regexp.MustCompile(`<!-- mem:id=(\S+)`)

// leafDay parses a leaf filename into its day key.
func leafDay(name string) (string, bool) {
	if !strings.HasSuffix(name, ".md") {
		return "", false
	}
	day := strings.TrimSuffix(name, ".md")
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return "", false
	}
	return day, true
}

// block is one captured entry in a leaf: header line, mem comment, body.
// eligible mirrors memory.Entry.PromotionEligible over the comment tokens.
type block struct {
	header  string // "### HH:MM — speaker"
	comment string // "<!-- mem:id=… -->"
	body    string // everything after the comment line
	id      string
	kind    string
	prov    string
	folded  bool
}

func (b block) eligible() bool {
	return b.kind == "interactive" && (b.prov == "owner" || b.prov == "agent")
}

const foldStub = "> _(folded — audit body removed by night prune)_"

// parseBlocks splits leaf text into the pre-first-block preamble plus the
// captured blocks. Text not matching the block shape stays verbatim inside
// the preamble or the previous block's body.
func parseBlocks(text string) (string, []block) {
	var blocks []block
	var pre strings.Builder
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) {
		if strings.HasPrefix(lines[i], "### ") && i+1 < len(lines) && memID.MatchString(lines[i+1]) {
			b := block{header: lines[i], comment: lines[i+1]}
			b.id = memID.FindStringSubmatch(lines[i+1])[1]
			for _, tok := range strings.Fields(lines[i+1]) {
				if strings.HasPrefix(tok, "kind=") {
					b.kind = strings.TrimPrefix(tok, "kind=")
				}
				if strings.HasPrefix(tok, "prov=") {
					b.prov = strings.TrimPrefix(tok, "prov=")
				}
			}
			i += 2
			var body strings.Builder
			for i < len(lines) && !strings.HasPrefix(lines[i], "### ") {
				body.WriteString(lines[i] + "\n")
				i++
			}
			b.body = body.String()
			b.folded = strings.Contains(b.body, foldStub)
			blocks = append(blocks, b)
			continue
		}
		pre.WriteString(lines[i] + "\n")
		i++
	}
	return pre.String(), blocks
}

// render assembles a leaf back — each block keeps its header and comment;
// a folded non-eligible block's body becomes the stub line.
func render(pre string, blocks []block) (string, int) {
	var b strings.Builder
	b.WriteString(pre)
	stubs := 0
	for _, bl := range blocks {
		b.WriteString(bl.header + "\n")
		b.WriteString(bl.comment + "\n")
		if !bl.eligible() && !bl.folded {
			b.WriteString(foldStub + "\n\n")
			stubs++
			continue
		}
		b.WriteString(bl.body)
	}
	return b.String(), stubs
}

// verifyFold is the auto-check before a fold commits: the folded text must
// preserve every mem:id and keep each eligible block byte-identical.
func verifyFold(orig, next string) error {
	count := func(s string) map[string]int {
		out := map[string]int{}
		for _, m := range memID.FindAllStringSubmatch(s, -1) {
			out[m[1]]++
		}
		return out
	}
	counts := count(next)
	for id, n := range count(orig) {
		if counts[id] != n {
			return fmt.Errorf("mem:id %s count changed %d -> %d", id, n, counts[id])
		}
	}
	_, ob := parseBlocks(orig)
	_, nb := parseBlocks(next)
	if len(ob) != len(nb) {
		return fmt.Errorf("block count changed %d -> %d", len(ob), len(nb))
	}
	for i := range ob {
		if ob[i].eligible() && ob[i].body != nb[i].body {
			return fmt.Errorf("eligible block %s body changed", ob[i].id)
		}
	}
	return nil
}

// foldLeaf rewrites one leaf stubbing non-eligible bodies through the
// governed writer — a concurrent owner edit wins the hash check.
func foldLeaf(w *vault.Writer, root, dayKey string) (*FoldResult, error) {
	rel := filepath.ToSlash(filepath.Join("memory", "daily", dayKey+".md"))
	abs := filepath.Join(root, filepath.FromSlash(rel))
	cur, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	pre, blocks := parseBlocks(string(cur))
	foldable := 0
	for _, bl := range blocks {
		if !bl.eligible() && !bl.folded {
			foldable++
		}
	}
	if foldable == 0 {
		return nil, nil
	}
	next, stubs := render(pre, blocks)
	if err := verifyFold(string(cur), next); err != nil {
		return nil, fmt.Errorf("fold verify %s: %w", dayKey, err)
	}
	if _, err := w.Apply(memory.WriteOp{
		Path: rel, Content: []byte(next), WantSHA: sha256Hex(cur),
		Actor: memory.ActorSystem, Message: "memory prune fold " + dayKey,
	}); err != nil && !errors.Is(err, memory.ErrStaleHash) {
		return nil, err
	} else if errors.Is(err, memory.ErrStaleHash) {
		return nil, nil
	}
	return &FoldResult{Day: dayKey, Before: len(cur), After: len(next), Stubs: stubs}, nil
}

// expireLeaf removes a verified leaf through git and drops the prov.db
// rows its mem:ids carry — same atomic rewrite shape as vacuum.
func expireLeaf(root, dayKey, rel string) error {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	cur, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, m := range memID.FindAllStringSubmatch(string(cur), -1) {
		ids[m[1]] = true
	}
	for _, args := range [][]string{
		{"rm", "-q", "--", rel},
		{"-c", "user.name=hermes-system", "-c", "user.email=system@vault.local",
			"commit", "--quiet", "-m", "vault: system prune expire " + dayKey},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("vault git %s: %w (%s)", args[0], err, out)
		}
	}
	return dropProvRows(root, ids)
}

// dropProvRows rewrites prov.db without the expired day's rows — atomic
// via temp+rename, same durability shape as the sidecar itself.
func dropProvRows(root string, drop map[string]bool) error {
	path := filepath.Join(root, "runtime", "memory", "prov.db")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var keep [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := append([]byte{}, sc.Bytes()...)
		var e struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(line, &e) == nil && drop[e.ID] {
			continue
		}
		keep = append(keep, line)
	}
	_ = f.Close()
	tmp := path + ".tmp"
	w, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, line := range keep {
		if _, err := w.Write(append(line, '\n')); err != nil {
			_ = w.Close()
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Apply folds every completed oversized leaf that still carries foldable
// audit bodies and expires verified retention-age leaves — today's leaf
// and unverified days are never touched.
func Apply(root string, o Options) (Report, error) {
	var rep Report
	tz := o.TZ
	if tz == nil {
		tz = time.UTC
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	today := now.In(tz).Format("2006-01-02")
	cutoff := now.In(tz).AddDate(0, 0, -o.RetentionDays).Format("2006-01-02")

	files, _ := os.ReadDir(filepath.Join(root, "memory", "daily"))
	w := vault.NewWriter(root)
	for _, f := range files {
		dayKey, ok := leafDay(f.Name())
		if !ok || dayKey >= today {
			continue
		}
		complete := night.DayComplete(root, dayKey)

		// EXPIRE lane — retention-age leaves go only when the rollup
		// completed AND the day summary exists on disk.
		if o.RetentionDays > 0 && dayKey <= cutoff {
			if !complete {
				rep.Kept = append(rep.Kept, KeptDay{Day: dayKey, Reason: "rollup incomplete"})
				_ = journalPrune(w, root, dayKey, []string{"kept leaf — rollup incomplete"})
				continue
			}
			summary := filepath.Join(root, "memory", "summaries", "daily", dayKey+".md")
			if _, err := os.Stat(summary); err != nil {
				rep.Kept = append(rep.Kept, KeptDay{Day: dayKey, Reason: "day summary missing"})
				continue
			}
			rel := filepath.ToSlash(filepath.Join("memory", "daily", f.Name()))
			if err := expireLeaf(root, dayKey, rel); err != nil {
				return rep, err
			}
			rep.Expired = append(rep.Expired, rel)
			_ = journalPrune(w, root, dayKey, []string{"expired leaf (retention)"})
			continue
		}

		// FOLD lane — only completed oversized leaves.
		info, err := f.Info()
		if err != nil {
			return rep, err
		}
		if o.FoldBytes <= 0 || info.Size() <= o.FoldBytes || !complete {
			continue
		}
		fr, err := foldLeaf(w, root, dayKey)
		if err != nil {
			return rep, err
		}
		if fr != nil {
			rep.Folded = append(rep.Folded, *fr)
			_ = journalPrune(w, root, dayKey, []string{
				fmt.Sprintf("folded leaf (%d→%d bytes, %d audit stubs)", fr.Before, fr.After, fr.Stubs)})
		}
	}
	return rep, nil
}

// Scan is the dry-run twin of Apply: it walks the same lanes and reports
// the same actions without touching a byte — `hubctl memory prune` prints
// this report when --apply is absent.
func Scan(root string, o Options) (Report, error) {
	var rep Report
	tz := o.TZ
	if tz == nil {
		tz = time.UTC
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	today := now.In(tz).Format("2006-01-02")
	cutoff := now.In(tz).AddDate(0, 0, -o.RetentionDays).Format("2006-01-02")

	files, _ := os.ReadDir(filepath.Join(root, "memory", "daily"))
	for _, f := range files {
		dayKey, ok := leafDay(f.Name())
		if !ok || dayKey >= today {
			continue
		}
		if o.RetentionDays > 0 && dayKey <= cutoff {
			if !night.DayComplete(root, dayKey) {
				rep.Kept = append(rep.Kept, KeptDay{Day: dayKey, Reason: "rollup incomplete"})
				continue
			}
			if _, err := os.Stat(filepath.Join(root, "memory", "summaries", "daily", dayKey+".md")); err != nil {
				continue
			}
			rep.Expired = append(rep.Expired, filepath.ToSlash(filepath.Join("memory", "daily", f.Name())))
			continue
		}
		info, err := f.Info()
		if err != nil {
			return rep, err
		}
		if o.FoldBytes <= 0 || info.Size() <= o.FoldBytes || !night.DayComplete(root, dayKey) {
			continue
		}
		cur, err := os.ReadFile(filepath.Join(root, "memory", "daily", f.Name()))
		if err != nil {
			return rep, err
		}
		_, blocks := parseBlocks(string(cur))
		foldable := 0
		for _, bl := range blocks {
			if !bl.eligible() && !bl.folded {
				foldable++
			}
		}
		if foldable > 0 {
			rep.Folded = append(rep.Folded, FoldResult{Day: dayKey, Before: int(info.Size()), Stubs: foldable})
		}
	}
	return rep, nil
}

// journalPrune appends one "## prune <dayKey>" section to REVIEW.md — the
// same audit surface the rollup journal writes, so kept and cut bytes
// land in the record. A rerun for the same day appends nothing (the
// section guard mirrors the journal's crash-resume rule).
func journalPrune(w *vault.Writer, root, dayKey string, lines []string) error {
	rel := "memory/REVIEW.md"
	abs := filepath.Join(root, filepath.FromSlash(rel))
	cur, err := os.ReadFile(abs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	section := "## prune " + dayKey
	if strings.Contains(string(cur), section) {
		return nil
	}
	var b strings.Builder
	b.Write(cur)
	if len(cur) == 0 {
		b.WriteString("# REVIEW — nightly rollup journal\n")
	}
	b.WriteString("\n" + section + "\n")
	for _, l := range lines {
		b.WriteString("\n- " + l)
	}
	b.WriteString("\n")
	want := ""
	if err == nil {
		want = sha256Hex(cur)
	}
	_, err = w.Apply(memory.WriteOp{Path: rel, Content: []byte(b.String()), WantSHA: want, Actor: memory.ActorSystem})
	return err
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
