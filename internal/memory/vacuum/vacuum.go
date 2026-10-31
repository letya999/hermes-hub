// Package vacuum is the M20.7 hard-purge operator lane (SPEC-0085 R7.4,
// ADR-0086 D16): it removes superseded cards and struck entries from the
// vault's working tree. Soft forget hides; vacuum deletes — git history
// always keeps the recoverable record.
package vacuum

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// Report lists what a scan found and (after Apply) what was purged.
type Report struct {
	SupersededCards []string `json:"superseded_cards"`
	StruckEntries   []string `json:"struck_entries"`
	LeafRewrites    []string `json:"leaf_rewrites"`
	ProvDropped     int      `json:"prov_dropped"`
	Applied         bool     `json:"applied"`
}

var memID = regexp.MustCompile(`<!-- mem:id=(\S+)`)

// Scan reports the purge set without touching anything: cards whose
// frontmatter status is superseded, leaf blocks whose entry id is struck in
// the forget sidecar, and those entries' prov.db rows.
func Scan(root string) (Report, error) {
	var rep Report
	struck := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Set()

	// Superseded cards — frontmatter status only, body text is data.
	cards := filepath.Join(root, "memory", "cards")
	_ = filepath.Walk(cards, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if memory.FrontmatterStatus(string(data)) == "superseded" {
			rel, _ := filepath.Rel(root, p)
			rep.SupersededCards = append(rep.SupersededCards, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(rep.SupersededCards)

	// Struck entry blocks inside daily leaves.
	dailyDir := filepath.Join(root, "memory", "daily")
	files, _ := os.ReadDir(dailyDir)
	struckSeen := map[string]bool{}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dailyDir, f.Name()))
		if err != nil {
			return rep, err
		}
		found := false
		for _, m := range memID.FindAllStringSubmatch(string(data), -1) {
			if struck[m[1]] {
				found = true
				struckSeen[m[1]] = true
			}
		}
		if found {
			rep.LeafRewrites = append(rep.LeafRewrites, filepath.ToSlash(filepath.Join("memory", "daily", f.Name())))
		}
	}
	sort.Strings(rep.LeafRewrites)
	for id := range struckSeen {
		rep.StruckEntries = append(rep.StruckEntries, id)
	}
	sort.Strings(rep.StruckEntries)

	// Struck prov.db rows drop with the entries they describe.
	provPath := filepath.Join(root, "runtime", "memory", "prov.db")
	if f, err := os.Open(provPath); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			var e struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(sc.Bytes(), &e) == nil && struck[e.ID] {
				rep.ProvDropped++
			}
		}
		f.Close()
	}
	return rep, nil
}

// Apply runs the scan's purge set: struck leaf blocks are stripped through
// the governed writer (hash-checked — a concurrent owner edit wins),
// superseded cards are removed, and the struck prov.db rows drop. Git
// history keeps everything recoverable.
func Apply(root string) (Report, error) {
	rep, err := Scan(root)
	if err != nil {
		return rep, err
	}
	struck := memory.OpenForgetSet(filepath.Join(root, "runtime", "memory")).Set()
	w := vault.NewWriter(root)
	for _, rel := range rep.LeafRewrites {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		cur, err := os.ReadFile(abs)
		if err != nil {
			return rep, err
		}
		if _, err := w.Apply(memory.WriteOp{
			Path: rel, Content: []byte(stripBlocks(string(cur), struck)), WantSHA: sha256Hex(cur),
			Actor: memory.ActorSystem, Message: "memory vacuum",
		}); err != nil {
			return rep, err
		}
	}
	for _, rel := range rep.SupersededCards {
		cmd := exec.Command("git", "rm", "-q", "--", rel)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return rep, fmt.Errorf("vault git rm %s: %w (%s)", rel, err, out)
		}
	}
	if len(rep.SupersededCards) > 0 {
		cmd := exec.Command("git", "-c", "user.name=hermes-system", "-c", "user.email=system@vault.local",
			"commit", "--quiet", "-m", "vault: system vacuum purge")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return rep, fmt.Errorf("vault git commit: %w (%s)", err, out)
		}
	}
	if rep.ProvDropped > 0 {
		if err := dropProvRows(root, struck); err != nil {
			return rep, err
		}
	}
	rep.Applied = true
	return rep, nil
}

// stripBlocks removes every leaf block whose entry id is struck: from the
// "### time — speaker" heading through the line before the next heading.
func stripBlocks(text string, struck map[string]bool) string {
	lines := strings.Split(text, "\n")
	var out []string
	skip := false
	for i, line := range lines {
		if strings.HasPrefix(line, "### ") {
			skip = false
			if i+1 < len(lines) {
				if m := memID.FindStringSubmatch(lines[i+1]); m != nil && struck[m[1]] {
					skip = true
					continue
				}
			}
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// dropProvRows rewrites prov.db without the struck rows — atomic via
// temp+rename, same durability shape as the sidecar itself.
func dropProvRows(root string, struck map[string]bool) error {
	path := filepath.Join(root, "runtime", "memory", "prov.db")
	f, err := os.Open(path)
	if err != nil {
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
		if json.Unmarshal(line, &e) == nil && struck[e.ID] {
			continue
		}
		keep = append(keep, line)
	}
	f.Close()
	tmp := path + ".tmp"
	w, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, line := range keep {
		if _, err := w.Write(append(line, '\n')); err != nil {
			w.Close()
			return err
		}
	}
	if err := w.Sync(); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
