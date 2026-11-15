package passport

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/locale"
)

// layoutNode is one typed workspace.layout entry (SPEC-0092 R5.1): the
// directory it materializes, an optional human note, an admission/display
// mode ("" or rw = writable-eligible, ro = read-only) and an optional
// placeholder rendered after the name for parametrized subtrees such as
// people/<channel>/<date>/<attachment-id>.
type layoutNode struct {
	Path        string
	Note        string
	Mode        string
	Placeholder string
}

// invariantReadOnly roots a space can never widen into writable
// (ADR-0092 D5): agent config and skills are compiled views, the inbox
// and service artifacts belong to ingress/services, and vault memory is
// owned by the memory pipeline — not by ad-hoc file writes.
var invariantReadOnly = []string{
	"/config",
	"/skills",
	"/workspace/inbox",
	"/workspace/artifacts/services",
	"/vault/memory",
}

// invariantWritable roots the fixed skeleton always mounts rw
// (ADR-0092 D5): the library and attachments half of the vault are the
// agent's own store, unlike the pipeline-owned memory tree. They join the
// emitted boundary as defaults — a declared read_only above them still
// narrows, and an explicit writable elsewhere stays untouched.
var invariantWritable = []string{
	"/vault/library",
	"/vault/attachments",
}

// str renders an optional map field — an absent key is "", never "<nil>".
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// parseLayout normalizes workspace.layout: entries are plain path strings
// or typed maps {path, note?, mode: ro|rw, placeholder?}.
func parseLayout(v any, owner string) ([]layoutNode, error) {
	items, _ := v.([]any)
	if v != nil && items == nil {
		return nil, fmt.Errorf("passport compile: layer %q workspace.layout expects a list", owner)
	}
	nodes := make([]layoutNode, 0, len(items))
	for _, it := range items {
		var n layoutNode
		switch raw := it.(type) {
		case string:
			n.Path = raw
		case map[string]any:
			n.Path = str(raw["path"])
			n.Note = str(raw["note"])
			n.Mode = str(raw["mode"])
			n.Placeholder = str(raw["placeholder"])
			if n.Mode != "" && n.Mode != "ro" && n.Mode != "rw" {
				return nil, fmt.Errorf("passport compile: layer %q workspace.layout %s mode %q must be ro or rw", owner, n.Path, n.Mode)
			}
		default:
			return nil, fmt.Errorf("passport compile: layer %q workspace.layout entry must be a path or {path,...}", owner)
		}
		if clean := path.Clean(n.Path); clean != n.Path || !strings.HasPrefix(n.Path, "/") {
			return nil, fmt.Errorf("passport compile: layer %q workspace.layout path %q is not a canonical absolute path", owner, n.Path)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// mergeWorkspaceLayout folds the layers' workspace.layout into the
// effective directory inventory. Ceiling layers author the bound key set;
// every deeper declaration must stay inside it — may drop nodes, mark
// mode ro, or re-annotate — but never adds paths or flips ro back to rw
// (SPEC-0092 R5.1: layers narrow, they do not widen).
func mergeWorkspaceLayout(layers []Layer) ([]layoutNode, error) {
	var bound map[string]layoutNode
	var effective []layoutNode
	for i, l := range layers {
		raw, ok := l.Docs["workspace"]["layout"]
		if !ok {
			continue
		}
		nodes, err := parseLayout(raw, l.Name)
		if err != nil {
			return nil, err
		}
		ceiling := i == 0 || i == 2
		merged := make([]layoutNode, 0, len(nodes))
		seen := map[string]bool{}
		for _, n := range nodes {
			if seen[n.Path] {
				return nil, fmt.Errorf("passport compile: layer %q workspace.layout repeats %s", l.Name, n.Path)
			}
			seen[n.Path] = true
			if bound != nil {
				b, ok := bound[n.Path]
				if !ok {
					return nil, fmt.Errorf("passport compile: layer %q adds workspace.layout path %s outside the ceiling", l.Name, n.Path)
				}
				if n.Mode == "" {
					n.Mode = b.Mode
				} else if b.Mode == "ro" && n.Mode != "ro" {
					return nil, fmt.Errorf("passport compile: layer %q widens workspace.layout %s from read-only", l.Name, n.Path)
				}
				if n.Note == "" {
					n.Note = b.Note
				}
				if n.Placeholder == "" {
					n.Placeholder = b.Placeholder
				}
			}
			merged = append(merged, n)
		}
		if ceiling {
			next := map[string]layoutNode{}
			for _, n := range merged {
				next[n.Path] = n
			}
			bound = next
		}
		effective = merged
	}
	return effective, nil
}

// readOnlySet collects every path file admission must treat as read-only:
// the deepest declared read_only list, every invariant root, layout nodes
// marked mode: ro, and ro mount targets.
func readOnlySet(ws map[string]any, nodes []layoutNode) map[string]bool {
	ro := map[string]bool{}
	for _, p := range asList(ws["read_only"]) {
		ro[fmt.Sprint(p)] = true
	}
	for _, p := range invariantReadOnly {
		ro[p] = true
	}
	for _, n := range nodes {
		if n.Mode == "ro" {
			ro[n.Path] = true
		}
	}
	for target, raw := range asMap(ws["mounts"]) {
		if m, _ := raw.(map[string]any); fmt.Sprint(m["mode"]) == "ro" {
			ro[target] = true
		}
	}
	return ro
}

// pathWithin reports whether p equals or descends from root.
func pathWithin(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+"/")
}

// ---- ASCII tree (ADR-0092 D5: the tree is derived, never authored) ----

type treeEntry struct {
	name string
	note string
	ro   bool
	kids []*treeEntry
}

func (e *treeEntry) child(name string) *treeEntry {
	for _, k := range e.kids {
		if k.name == name {
			return k
		}
	}
	k := &treeEntry{name: name}
	e.kids = append(e.kids, k)
	return k
}

// renderTree draws the effective layout as a box-drawing map rooted at /.
// The fixed skeleton — /config's passport files, /vault's memory skeleton,
// /skills — is part of the runtime contract and renders even when no
// layout node names it; layout nodes annotate and extend it, and declared
// mounts appear under /shared.
func renderTree(nodes []layoutNode, mounts map[string]any, lang locale.Lang) string {
	// Root order is the normative map: config, workspace, vault, skills,
	// shared — layout nodes attach under them, they do not reorder roots.
	root := &treeEntry{}
	cfg := root.child("config/")
	cfg.ro = true
	for _, f := range Names {
		cfg.child(f + ".yaml")
	}
	prompt := cfg.child("system-prompt.md")
	prompt.note = locale.T(lang, "workspace.note.system_prompt")
	cfg.child("SOUL.md")
	cfg.child("USER.md")
	cfg.child("AGENTS.md")
	root.child("workspace/")
	vault := root.child("vault/")
	mem := vault.child("memory/")
	mem.ro = true
	for _, f := range []string{"CORE.md", "CORE.history.md", "daily/", "cards/{contacts,projects,decisions,ideas,notes}/", "summaries/{daily,weekly,monthly,yearly}/"} {
		mem.child(f)
	}
	vault.child("library/")
	vault.child("attachments/")
	skills := root.child("skills/")
	skills.ro = true
	skills.note = locale.T(lang, "workspace.note.skills")

	for _, n := range nodes {
		cur := root
		parts := strings.Split(strings.Trim(n.Path, "/"), "/")
		for i, seg := range parts {
			name := seg + "/"
			if i == len(parts)-1 && n.Placeholder != "" {
				name += strings.Trim(n.Placeholder, "/") + "/"
			}
			cur = cur.child(name)
		}
		if n.Note != "" {
			cur.note = n.Note
		}
		if n.Mode == "ro" {
			cur.ro = true
		}
	}
	shared := root.child("shared/")
	targets := make([]string, 0, len(mounts))
	for t := range mounts {
		targets = append(targets, t)
	}
	sort.Strings(targets)
	for _, t := range targets {
		m, _ := mounts[t].(map[string]any)
		e := shared.child(strings.TrimPrefix(strings.Trim(t, "/"), "shared/") + "/")
		if fmt.Sprint(m["mode"]) == "ro" {
			e.ro = true
		}
	}
	if len(shared.kids) == 0 {
		root.kids = removeEntry(root.kids, shared)
	}

	marker := locale.T(lang, "workspace.marker.read_only")
	col := 0
	var measure func(e *treeEntry, depth int)
	measure = func(e *treeEntry, depth int) {
		if w := depth*4 + len(e.name); w > col {
			col = w
		}
		for _, k := range e.kids {
			measure(k, depth+1)
		}
	}
	for _, k := range root.kids {
		measure(k, 1)
	}
	col += 2
	lines := []string{"/"}
	var walk func(e *treeEntry, prefix string, last bool)
	walk = func(e *treeEntry, prefix string, last bool) {
		branch := "├── "
		if last {
			branch = "└── "
		}
		name := prefix + branch + e.name
		ann := e.note
		if e.ro {
			if ann != "" {
				ann = marker + "; " + ann
			} else {
				ann = marker
			}
		}
		if pad := col - len(name); ann != "" && pad > 0 {
			name += strings.Repeat(" ", pad) + ann
		} else if ann != "" {
			name += "  " + ann
		}
		lines = append(lines, name)
		next := prefix + "│   "
		if last {
			next = prefix + "    "
		}
		for i, k := range e.kids {
			walk(k, next, i == len(e.kids)-1)
		}
	}
	for i, k := range root.kids {
		walk(k, "", i == len(root.kids)-1)
	}
	return strings.Join(lines, "\n")
}

func removeEntry(kids []*treeEntry, drop *treeEntry) []*treeEntry {
	out := kids[:0]
	for _, k := range kids {
		if k != drop {
			out = append(out, k)
		}
	}
	return out
}
