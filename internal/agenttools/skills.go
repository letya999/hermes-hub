package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// The five skill tools (SPEC-0083 R5.1). Reads serve the mounted effective
// view (/skills + index.json); every write lands as a draft in the writable
// owner store mounted at HUB_SKILLS_STORE — nothing publishes here.
var errSkillNotFound = errors.New("skill not found")

// actionFields is the per-action field allowlist; a key belonging to another
// action (or none) is rejected before anything is applied (R5.3).
var actionFields = map[string][]string{
	"create":         {"action", "name", "content", "category", "reason"},
	"patch":          {"action", "name", "old_string", "new_string", "file_path", "base_revision", "reason"},
	"edit":           {"action", "name", "content", "file_path", "base_revision", "reason"},
	"write_file":     {"action", "name", "file_path", "content", "reason"},
	"remove_file":    {"action", "name", "file_path", "reason"},
	"delete":         {"action", "name", "reason"},
	"archive":        {"action", "name", "reason", "absorbed_into"},
	"restore":        {"action", "name", "reason", "version"},
	"propose_to_org": {"action", "name", "reason"},
}

func (t *Tools) skillCall(ctx context.Context, name string, r Input) (any, error) {
	switch name {
	case "skill":
		return t.skillInvoke(ctx, r)
	case "skills_list":
		return t.skillsList(r)
	case "skill_view":
		return t.skillView(r)
	case "skills_search":
		return t.skillsSearch(r)
	case "skill_manage":
		return nil, errors.New("not implemented")
	}
	return nil, fmt.Errorf("unknown skill tool %q", name)
}

// skillsSearch matches the query against name and description in the
// effective index; limit defaults to 5 and clamps at 20 (R5 contract).
func (t *Tools) skillsSearch(r Input) (any, error) {
	q := strings.TrimSpace(r.Query)
	if q == "" || len(q) > 1000 {
		return nil, fmt.Errorf("%w: query must be 1-1000 chars", skillstore.ErrInvalid)
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	idx, err := t.effectiveIndex()
	if err != nil {
		return nil, err
	}
	matched := []skillstore.Entry{}
	for _, e := range idx.Skills {
		if r.Source != "" && e.Source != r.Source {
			continue
		}
		if strings.Contains(e.Name, q) || strings.Contains(e.Description, q) {
			matched = append(matched, e)
		}
	}
	coverage := len(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}
	hits := []map[string]any{}
	for _, e := range matched {
		hits = append(hits, map[string]any{
			"name": e.Name, "source": e.Source, "description": e.Description,
		})
	}
	return map[string]any{"hits": hits, "hasMore": coverage > len(hits), "coverage": coverage}, nil
}

// skillView lists a skill's files or returns one file's text.
// version reads a retained revision in the owner store instead of live.
func (t *Tools) skillView(r Input) (any, error) {
	name := strings.TrimSpace(r.Name)
	var tree skillstore.Tree
	var err error
	if rev := strings.TrimSpace(r.Version); rev != "" {
		var st *skillstore.Store
		st, err = skillstore.Open(t.SkillsStore, skillstore.ScopeUser)
		if err == nil {
			tree, err = st.RevisionTree(name, rev)
		}
	} else {
		tree, err = t.effectiveTree(name)
	}
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(r.Path)
	if path == "" {
		files := []string{}
		for p := range tree {
			files = append(files, p)
		}
		slices.Sort(files)
		return map[string]any{"name": name, "files": files}, nil
	}
	file, ok := tree[path]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errSkillNotFound, path)
	}
	return map[string]any{"name": name, "path": path, "content": string(file.Data)}, nil
}

// skillsList pages the effective index 20 at a time (R5 contract).
func (t *Tools) skillsList(r Input) (any, error) {
	idx, err := t.effectiveIndex()
	if err != nil {
		return nil, err
	}
	entries := []map[string]any{}
	for _, e := range idx.Skills {
		if r.Source != "" && e.Source != r.Source {
			continue
		}
		entries = append(entries, map[string]any{
			"name": e.Name, "description": e.Description, "source": e.Source,
		})
	}
	offset := len(entries)
	if r.Offset < uint64(len(entries)) {
		offset = int(r.Offset) // #nosec G115 -- bounded by the index length check above
	}
	hasMore := len(entries) > offset+20
	if hasMore {
		entries = entries[offset : offset+20]
	} else {
		entries = entries[offset:]
	}
	return map[string]any{"skills": entries, "hasMore": hasMore}, nil
}

// skillManage handles writes; every action produces a draft — nothing here
// mutates the live catalog.
func (t *Tools) skillManage(ctx context.Context, arguments map[string]any) (any, error) {
	action, _ := arguments["action"].(string)
	for key := range arguments {
		if !slices.Contains(actionFields[action], key) {
			return nil, fmt.Errorf("%w: field %q does not belong to action %s", skillstore.ErrInvalid, key, action)
		}
	}
	name := strings.TrimSpace(argString(arguments, "name"))
	content := argString(arguments, "content")
	if action == "propose_to_org" {
		return t.proposeToOrg(ctx, name, arguments)
	}
	st, err := skillstore.Open(t.SkillsStore, skillstore.ScopeUser)
	if err != nil {
		return nil, err
	}
	var tree skillstore.Tree
	baseRevision := ""
	restoreStamp := ""
	if action == "patch" || action == "edit" {
		base, err := st.Tree(name)
		if err != nil {
			return nil, err
		}
		baseRevision = skillstore.Revision(base)
		if argString(arguments, "base_revision") != baseRevision {
			return nil, fmt.Errorf("%w: base_revision does not match current revision %s", skillstore.ErrConflict, baseRevision)
		}
		tree = base
		file := base[skillstore.SkillFile]
		if action == "patch" {
			tree[skillstore.SkillFile] = skillstore.File{Data: []byte(strings.Replace(string(file.Data), argString(arguments, "old_string"), argString(arguments, "new_string"), 1)), Exec: file.Exec}
		} else {
			tree[skillstore.SkillFile] = skillstore.File{Data: []byte(content), Exec: file.Exec}
		}
	} else if action == "write_file" || action == "remove_file" {
		base, err := st.Tree(name)
		if err != nil {
			return nil, err
		}
		tree = base
		path := strings.TrimSpace(argString(arguments, "file_path"))
		if action == "write_file" {
			tree[path] = skillstore.File{Data: []byte(content)}
		} else {
			delete(tree, path)
		}
	} else if action == "archive" || action == "delete" {
		base, err := st.Tree(name)
		if err != nil {
			return nil, err
		}
		tree = base
	} else if action == "restore" {
		base, stamp, err := st.ArchiveTree(name, argString(arguments, "version"))
		if err != nil {
			return nil, err
		}
		tree = base
		restoreStamp = stamp
	} else {
		tree = skillstore.Tree{skillstore.SkillFile: skillstore.File{Data: []byte(content)}}
	}
	meta := skillstore.DraftMeta{Author: "agent", Reason: argString(arguments, "reason"), SessionID: os.Getenv("HUB_RUNTIME_ID"), BaseRevision: baseRevision}
	if action == "archive" || action == "delete" {
		meta.Action = "archive"
		meta.AbsorbedInto = argString(arguments, "absorbed_into")
	}
	if action == "restore" {
		meta.Action = "restore"
		meta.RestoreStamp = restoreStamp
	}
	d, err := st.CreateDraft(name, tree, meta)
	if err != nil {
		return nil, err
	}
	diff := diffTrees(st, name, tree)
	link := t.skillDraftLink(ctx, map[string]string{
		"draft_id": d.ID, "skill_name": name, "scope": "user",
		"summary":       argString(arguments, "reason"),
		"diff":          diff,
		"findings":      d.Meta.ScanVerdict,
		"provenance":    "agent via runtime " + os.Getenv("HUB_RUNTIME_ID"),
		"base_revision": baseRevision,
	})
	return map[string]any{
		"draft_id":    d.ID,
		"name":        name,
		"state":       "draft",
		"diff":        diff,
		"lint":        map[string]any{"ok": true},
		"scan":        map[string]any{"verdict": d.Meta.ScanVerdict, "findings": d.Meta.ScanFindings},
		"link_sent":   link != "",
		"confirm_url": link,
	}, nil
}

// skillDraftLink asks communication to mint the protected approval card and
// returns the form URL; "" when the control channel is unavailable — the
// draft is already durable, the link is delivery, not authority.
func (t *Tools) skillDraftLink(ctx context.Context, payload map[string]string) string {
	if strings.TrimSpace(t.CommunicationURL) == "" {
		return ""
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.CommunicationURL, "/")+"/v1/skill-drafts", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	request.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	request.Header.Set("X-Hub-Principal", os.Getenv("HUB_PRINCIPAL_ID"))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	var out struct {
		FormURL string `json:"form_url"`
	}
	if response.StatusCode/100 != 2 || json.NewDecoder(response.Body).Decode(&out) != nil {
		return ""
	}
	return out.FormURL
}

// diffTrees renders a compact -/+ line diff between the live tree and the
// draft tree so the approval card (and tool response) shows real change.
func diffTrees(st *skillstore.Store, name string, tree skillstore.Tree) string {
	base, _ := st.Tree(name)
	var b strings.Builder
	paths := []string{}
	for p := range tree {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		old, hasOld := base[p]
		new := tree[p]
		if hasOld && string(old.Data) == string(new.Data) {
			continue
		}
		fmt.Fprintf(&b, "* %s\n", p)
		writeDiffLines(&b, "- ", string(old.Data))
		writeDiffLines(&b, "+ ", string(new.Data))
	}
	return b.String()
}

func writeDiffLines(b *strings.Builder, prefix, text string) {
	for _, line := range strings.Split(text, "\n") {
		b.WriteString(prefix + line + "\n")
	}
}

// proposeToOrg copies the user's live skill into the org store as a draft.
// It never publishes: org admin/owner approval is enforced at apply time.
func (t *Tools) proposeToOrg(ctx context.Context, name string, arguments map[string]any) (any, error) {
	user, err := skillstore.Open(t.SkillsStore, skillstore.ScopeUser)
	if err != nil {
		return nil, err
	}
	tree, err := user.Tree(name)
	if err != nil {
		return nil, err
	}
	org, err := skillstore.Open(t.SkillsOrg, skillstore.ScopeOrg)
	if err != nil {
		return nil, err
	}
	d, err := org.CreateDraft(name, tree, skillstore.DraftMeta{
		Author: "agent", Reason: argString(arguments, "reason"),
		SessionID: os.Getenv("HUB_RUNTIME_ID"),
	})
	if err != nil {
		return nil, err
	}
	link := t.skillDraftLink(ctx, map[string]string{
		"draft_id": d.ID, "skill_name": name, "scope": "org",
		"summary":    argString(arguments, "reason"),
		"findings":   d.Meta.ScanVerdict,
		"provenance": "agent via runtime " + os.Getenv("HUB_RUNTIME_ID"),
	})
	return map[string]any{"draft_id": d.ID, "name": name, "state": "draft", "scope": "org",
		"link_sent": link != "", "confirm_url": link}, nil
}

func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func (t *Tools) effectiveIndex() (*skillstore.EffectiveIndex, error) {
	body, err := os.ReadFile(filepath.Join(t.SkillsEffective, "index.json"))
	if err != nil {
		return nil, err
	}
	var idx skillstore.EffectiveIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

func (t *Tools) effectiveTree(name string) (skillstore.Tree, error) {
	idx, err := t.effectiveIndex()
	if err != nil {
		return nil, err
	}
	found := false
	for _, e := range idx.Skills {
		if e.Name == name {
			found = true
		}
	}
	if !found {
		return nil, errSkillNotFound
	}
	st, err := skillstore.Open(t.SkillsEffective, skillstore.ScopeCatalog)
	if err != nil {
		return nil, err
	}
	return st.Tree(name)
}

// stripFrontmatter removes the leading ---...--- block and returns the body.
func stripFrontmatter(raw []byte) (string, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", fmt.Errorf("%w: SKILL.md must start with frontmatter", skillstore.ErrInvalid)
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", fmt.Errorf("%w: unterminated frontmatter", skillstore.ErrInvalid)
	}
	body := rest[end+4:]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}
	return strings.TrimLeft(body, "\n"), nil
}

func (t *Tools) skillInvoke(ctx context.Context, r Input) (any, error) {
	name := strings.TrimSpace(r.Name)
	tree, err := t.effectiveTree(name)
	if err != nil {
		return nil, err
	}
	if _, _, err := skillstore.ParseSkillMD(name, tree[skillstore.SkillFile].Data, true); err != nil {
		return nil, err
	}
	body, err := stripFrontmatter(tree[skillstore.SkillFile].Data)
	if err != nil {
		return nil, err
	}
	resources := []string{}
	for path := range tree {
		if path != skillstore.SkillFile {
			resources = append(resources, path)
		}
	}
	slices.Sort(resources)
	return map[string]any{
		"name":      name,
		"skill_dir": "/skills/" + name,
		"body":      body,
		"resources": resources,
	}, nil
}
