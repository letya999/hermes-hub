// Package importer is the M20.7 native-import lane (SPEC-0085 R7.1): it
// parses the pre-vault MEMORY.md/USER.md into draft cards staged under the
// hub-owned runtime/ sidecar zone. Nothing lands in a governed zone until an
// explicit Apply commits each staged card through the vault writer.
package importer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// CardDraft is one section of a native file rendered as a card file.
type CardDraft struct {
	Name  string   `json:"name"` // card slug; staged and applied as <name>.md
	Facts []string `json:"facts"`
	From  string   `json:"from"` // source file basename
}

// Report is the reviewable output of Parse.
type Report struct {
	Files      []string    `json:"files"`
	Cards      []CardDraft `json:"cards"`
	Directives []string    `json:"directives"` // review-only CORE.md candidates
}

// ApplyResult reports which staged drafts committed and which were skipped
// because the target card already exists (imports never clobber).
type ApplyResult struct {
	Applied []string `json:"applied"`
	Skipped []string `json:"skipped"`
}

// nativeFiles are the upstream memory files kept untouched by D10.
var nativeFiles = []string{"MEMORY.md", "USER.md"}

// directiveHeads names sections whose items are preference/rule statements —
// candidates for the owner-written CORE.md, never applied by the importer.
var directiveHeads = map[string]bool{
	"directives": true, "directive": true,
	"rules": true, "rule": true,
	"instructions": true, "instruction": true,
	"preferences": true, "preference": true,
}

var headRe = regexp.MustCompile(`^#{1,6}\s+(.*\S)\s*$`)
var bulletRe = regexp.MustCompile(`^[-*+]\s+(.*\S)\s*$`)

// Parse reads the native memory files under memDir into card drafts and
// directive candidates. Items under a directive-named section go to the
// candidates list only; everything else becomes a card fact.
func Parse(memDir string) (Report, error) {
	var rep Report
	used := map[string]bool{}
	for _, name := range nativeFiles {
		data, err := os.ReadFile(filepath.Join(memDir, name))
		if err != nil {
			continue // absent files are not an error — nothing to migrate
		}
		rep.Files = append(rep.Files, name)
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		section := stem // preamble content files under the file's own card
		facts := map[string][]string{}
		var order []string
		add := func(item string) {
			item = oneLine(item)
			if item == "" || item == "---" {
				return
			}
			if _, ok := facts[section]; !ok {
				order = append(order, section)
			}
			facts[section] = append(facts[section], item)
		}
		sc := bufio.NewScanner(strings.NewReader(string(data)))
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if m := headRe.FindStringSubmatch(line); m != nil {
				section = m[1]
				continue
			}
			if m := bulletRe.FindStringSubmatch(line); m != nil {
				add(m[1])
			} else {
				add(line)
			}
		}
		for _, sec := range order {
			if directiveHeads[strings.ToLower(sec)] {
				for _, f := range facts[sec] {
					rep.Directives = append(rep.Directives, name+" §"+sec+": "+f)
				}
				continue
			}
			base := slugify(stem + "-" + sec)
			slug := base
			for n := 2; used[slug]; n++ {
				slug = fmt.Sprintf("%s-%d", base, n)
			}
			used[slug] = true
			rep.Cards = append(rep.Cards, CardDraft{Name: slug, Facts: facts[sec], From: name})
		}
	}
	return rep, nil
}

func stageDir(vaultDir string) string {
	return filepath.Join(vaultDir, "runtime", "memory", "import")
}

// Stage writes the report to the hub-owned sidecar zone as the literal card
// files an Apply would commit, plus a directives review sheet. Staging is
// outside every governed zone and outside the index, so nothing is visible
// to search or recall until applied. Re-staging replaces the prior staging.
func Stage(vaultDir string, rep Report) error {
	dir := stageDir(vaultDir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	day := time.Now().UTC().Format("2006-01-02")
	for _, c := range rep.Cards {
		if err := os.WriteFile(filepath.Join(dir, c.Name+".md"), []byte(cardBody(c, day)), 0o600); err != nil {
			return err
		}
	}
	var dv strings.Builder
	dv.WriteString("# Directive candidates — import review (never applied)\n\n")
	for _, d := range rep.Directives {
		dv.WriteString("- " + d + "\n")
	}
	dv.WriteString("\nTo adopt, copy accepted lines into memory/CORE.md and commit as owner.\n")
	if err := os.WriteFile(filepath.Join(dir, "directives.md"), []byte(dv.String()), 0o600); err != nil {
		return err
	}
	js, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), js, 0o600)
}

// Staged lists the card drafts awaiting apply (names without .md).
func Staged(vaultDir string) ([]string, error) {
	entries, err := os.ReadDir(stageDir(vaultDir))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && e.Name() != "directives.md" {
			out = append(out, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Apply commits every staged draft card through the governed writer into
// memory/cards/notes/inbox/. An existing target is skipped untouched — the
// import is additive and can never clobber a card. Content is taken verbatim:
// the owner's edits to staged drafts are exactly what lands.
func Apply(vaultDir string) (ApplyResult, error) {
	var res ApplyResult
	dir := stageDir(vaultDir)
	names, err := Staged(vaultDir)
	if err != nil {
		return res, err
	}
	w := vault.NewWriter(vaultDir)
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name+".md"))
		if err != nil {
			return res, err
		}
		rel := "memory/cards/notes/inbox/" + name + ".md"
		if _, err := os.Stat(filepath.Join(vaultDir, filepath.FromSlash(rel))); err == nil {
			res.Skipped = append(res.Skipped, rel)
			continue
		}
		if _, err := w.Apply(memory.WriteOp{Path: rel, Content: content, WantSHA: "", Actor: memory.ActorSystem, Message: "memory import"}); err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, rel)
	}
	return res, os.RemoveAll(dir)
}

// cardBody renders the literal card file for one draft.
func cardBody(c CardDraft, day string) string {
	var b strings.Builder
	b.WriteString("---\ntype: note\nstatus: active\nsource: migrated\ntags: [migrated]\ncreated: " + day + "\nupdated: " + day + "\n---\n\n## Facts\n\n")
	for _, f := range c.Facts {
		b.WriteString("- " + day + ": " + f + " · [src: " + c.From + "]\n")
	}
	return b.String()
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "imported"
	}
	return s
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
