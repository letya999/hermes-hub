package search

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/memory"
)

// Indexer keeps one lazily rebuilt Index per vault — the mtime+size cache is
// a hub-owned sidecar under runtime/memory/, never inside the agent-visible
// markdown tree (SPEC-0085 R8.2).
type Indexer struct {
	root string
	idx  *Index
	fp   string
}

func NewIndexer(vaultRoot string) *Indexer {
	return &Indexer{root: vaultRoot}
}

// Ensure returns the index, rebuilding only when source fingerprints changed.
// The bool reports whether this call rebuilt — the tool surfaces that as a
// degraded status rather than hiding it.
func (x *Indexer) Ensure() (*Index, bool, error) {
	sources, err := x.sources()
	if err != nil {
		return nil, false, err
	}
	fp := fingerprint(sources)
	if x.idx != nil && x.fp == fp {
		return x.idx, false, nil
	}
	if x.idx == nil {
		if idx, snapFP := x.load(); idx != nil && snapFP == fp {
			x.idx, x.fp = idx, fp
			return x.idx, false, nil
		}
	}
	idx := &Index{}
	blocks := x.leafBlocks()
	provLines := x.provLines()
	for _, e := range x.entries() {
		d := Doc{
			ID:         e.ID,
			Text:       e.Body,
			Kind:       string(e.Kind),
			Audience:   string(e.Audience),
			Provenance: string(e.Provenance),
			Date:       e.ObservedAt.Format("2006-01-02"),
			Links:      linkRefs(e.Body),
		}
		if b, ok := blocks[e.ID]; ok {
			d.Path, d.Line, d.EndLine = b.path, b.start, b.end
			d.Date = strings.TrimSuffix(filepath.Base(b.path), ".md")
		} else {
			// Partial capture — prov.db row landed without a leaf block.
			// Evidence still points at durable bytes, never orphan text.
			d.Path = "runtime/memory/prov.db"
			d.Line, d.EndLine = provLines[e.ID], provLines[e.ID]
		}
		idx.Add(d)
	}
	for _, d := range x.markdownDocs(sources) {
		idx.Add(d)
	}
	x.persist(idx, fp)
	x.idx, x.fp = idx, fp
	return idx, true, nil
}

// snapshot is the persisted cache under runtime/memory/index/ — hub-owned
// sidecar, never inside the agent-visible tree.
type snapshot struct {
	Fingerprint string `json:"fingerprint"`
	Docs        []Doc  `json:"docs"`
}

func (x *Indexer) persist(idx *Index, fp string) {
	dir := filepath.Join(x.root, "runtime", "memory", "index")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	body, err := json.Marshal(snapshot{Fingerprint: fp, Docs: idx.docs})
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, "index.json.tmp")
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(dir, "index.json"))
}

func (x *Indexer) load() (*Index, string) {
	data, err := os.ReadFile(filepath.Join(x.root, "runtime", "memory", "index", "index.json"))
	if err != nil {
		return nil, ""
	}
	var snap snapshot
	if json.Unmarshal(data, &snap) != nil {
		return nil, ""
	}
	idx := &Index{}
	for _, d := range snap.Docs {
		idx.Add(d)
	}
	return idx, snap.Fingerprint
}

// markdownDocs indexes card and summary files as private whole-file docs —
// vault markdown is the owner's tree, never a group surface. Doc IDs are the
// file stems so a [[name]] link expands to memory/cards/**/name.md directly.
func (x *Indexer) markdownDocs(sources []source) []Doc {
	var out []Doc
	for _, s := range sources {
		isCard := strings.HasPrefix(s.path, "memory/cards/")
		isSummary := strings.HasPrefix(s.path, "memory/summaries/")
		if (!isCard && !isSummary) || !strings.HasSuffix(s.path, ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(x.root, filepath.FromSlash(s.path)))
		if err != nil {
			continue
		}
		text := string(data)
		kind := "card"
		if isSummary {
			kind = "summary"
		}
		out = append(out, Doc{
			ID:       strings.TrimSuffix(filepath.Base(s.path), ".md"),
			Path:     s.path,
			Line:     1,
			EndLine:  len(strings.Split(text, "\n")),
			Kind:     kind,
			Audience: "private",
			Links:    linkRefs(text),
			Text:     text,
		})
	}
	return out
}

// Stale reports whether indexed sources changed since the last persisted
// build — the doctor check surfaces it as a stale index (SPEC-0085 R3.6).
func (x *Indexer) Stale() (bool, error) {
	sources, err := x.sources()
	if err != nil {
		return false, err
	}
	fp := fingerprint(sources)
	if x.idx != nil && x.fp == fp {
		return false, nil
	}
	_, snapFP := x.load()
	return snapFP != fp, nil
}

// source is one indexed file and its fingerprint inputs.
type source struct {
	path  string // vault-relative
	mtime int64
	size  int64
}

func (x *Indexer) sources() ([]source, error) {
	if _, err := os.Stat(x.root); err != nil {
		return nil, fmt.Errorf("vault unavailable: %w", err)
	}
	var out []source
	add := func(rel string, info os.FileInfo) {
		out = append(out, source{path: filepath.ToSlash(rel), mtime: info.ModTime().Unix(), size: info.Size()})
	}
	prov := filepath.Join(x.root, "runtime", "memory", "prov.db")
	if info, err := os.Stat(prov); err == nil {
		rel, _ := filepath.Rel(x.root, prov)
		add(rel, info)
	}
	root := filepath.Join(x.root, "memory")
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".graph" || info.Name() == "attachments" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") || info.Name() == "CORE.md" || info.Name() == "CORE.history.md" {
			return nil
		}
		rel, err := filepath.Rel(x.root, p)
		if err != nil {
			return err
		}
		add(rel, info)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("vault unavailable: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

func fingerprint(sources []source) string {
	h := sha256.New()
	for _, s := range sources {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", s.path, s.mtime, s.size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// provLines maps entry IDs to their 1-based line inside prov.db — the
// fallback evidence ref when an entry has no leaf block (partial capture).
func (x *Indexer) provLines() map[string]int {
	out := map[string]int{}
	f, err := os.Open(filepath.Join(x.root, "runtime", "memory", "prov.db"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		var e struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" {
			out[e.ID] = line
		}
	}
	return out
}

func (x *Indexer) entries() []memory.Entry {
	prov, err := memory.OpenProvDB(filepath.Join(x.root, "runtime", "memory"))
	if err != nil {
		return nil
	}
	entries, err := prov.Entries()
	if err != nil {
		return nil
	}
	return entries
}

// block maps one captured entry to its daily-leaf evidence span.
type block struct {
	path       string
	start, end int
}

var memID = regexp.MustCompile(`<!-- mem:id=(\S+)`)

// leafBlocks scans daily leaves for mem:id comment markers and bounds each
// block from its metadata comment to the last line before the next block.
func (x *Indexer) leafBlocks() map[string]block {
	out := map[string]block{}
	dir := filepath.Join(x.root, "memory", "daily")
	files, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		abs := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("memory", "daily", f.Name()))
		lines := strings.Split(string(data), "\n")
		var curID string
		var curStart int
		closeAt := func(end int) {
			if curID == "" {
				return
			}
			for end > curStart && strings.TrimSpace(lines[end-1]) == "" {
				end--
			}
			out[curID] = block{path: rel, start: curStart, end: end}
			curID = ""
		}
		for i, line := range lines {
			if m := memID.FindStringSubmatch(line); m != nil {
				closeAt(i) // previous block ends on the line before
				curID, curStart = m[1], i+1
			}
		}
		closeAt(len(lines))
	}
	return out
}

var linkRef = regexp.MustCompile(`\[\[([^\]\[]+)\]\]`)

func linkRefs(text string) []string {
	var out []string
	for _, m := range linkRef.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}
