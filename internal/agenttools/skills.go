package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

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
	"configure":      {"action", "name", "values"},
	"install":        {"action", "url", "ref", "path", "name", "grant", "reason"},
	"update":         {"action", "name", "reason"},
	"check_updates":  {"action"},
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
	type hit struct {
		e     skillstore.Entry
		exact bool
	}
	matched := []hit{}
	for _, e := range idx.Skills {
		if e.Hidden || !e.Eligible {
			continue
		}
		if r.Source != "" && e.Source != r.Source {
			continue
		}
		if ok, exact := t.searchMatch(e, q); ok {
			matched = append(matched, hit{e: e, exact: exact})
		}
	}
	slices.SortFunc(matched, func(a, b hit) int {
		if a.exact != b.exact {
			if a.exact {
				return -1
			}
			return 1
		}
		return strings.Compare(a.e.Name, b.e.Name)
	})
	coverage := len(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}
	hits := []map[string]any{}
	for _, m := range matched {
		hits = append(hits, map[string]any{
			"name": m.e.Name, "source": m.e.Source, "description": m.e.Description,
		})
	}
	return map[string]any{"hits": hits, "hasMore": coverage > len(hits), "coverage": coverage}, nil
}

// lexTokens splits on non-letter/non-digit — RU+EN share the same rule
// (unicode letters), so no per-language dictionary is needed (R6.4).
func lexTokens(text string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		out[tok] = true
	}
	return out
}

// searchMatch reports whether the entry answers the query and whether the
// query names it exactly (exact hits sort first, R6.4). Tokens match name,
// description, tags and the skill body; a whole-phrase substring on
// name/description also matches.
func (t *Tools) searchMatch(e skillstore.Entry, q string) (bool, bool) {
	exact := e.Name == q
	if strings.Contains(e.Name, q) || strings.Contains(e.Description, q) {
		return true, exact
	}
	tagToks := map[string]bool{}
	for _, tag := range e.Tags {
		for tok := range lexTokens(tag) {
			tagToks[tok] = true
		}
	}
	var bodyToks map[string]bool
	for tok := range lexTokens(q) {
		if tagToks[tok] {
			return true, exact
		}
		if bodyToks == nil {
			bodyToks = lexTokens(t.skillBody(e.Name))
		}
		if bodyToks[tok] {
			return true, exact
		}
	}
	return false, exact
}

func (t *Tools) skillBody(name string) string {
	raw, err := os.ReadFile(filepath.Join(t.SkillsEffective, name, skillstore.SkillFile))
	if err != nil {
		return ""
	}
	body, _ := stripFrontmatter(raw)
	return body
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
	if !ok || !skillViewable(path) {
		return nil, fmt.Errorf("%w: %s", errSkillNotFound, path)
	}
	content := file.Data
	truncated := len(content) > skillViewMaxBytes
	if truncated {
		content = content[:skillViewMaxBytes]
	}
	return map[string]any{"name": name, "path": path, "content": string(content), "truncated": truncated}, nil
}

// skillViewMaxBytes bounds one resource view so a fat file cannot blow up the
// activation context (R7.1).
const skillViewMaxBytes = 32 * 1024

// skillViewable reports whether a tree path may be returned as text — SKILL.md
// and the resource dirs only; scripts/assets live on the skills: root (R7.1).
func skillViewable(path string) bool {
	if path == skillstore.SkillFile {
		return true
	}
	dir, _, _ := strings.Cut(path, "/")
	return dir == "references" || dir == "templates" || dir == "examples"
}

// skillsList pages the effective index 20 at a time (R5 contract).
func (t *Tools) skillsList(r Input) (any, error) {
	idx, err := t.effectiveIndex()
	if err != nil {
		return nil, err
	}
	entries := []map[string]any{}
	for _, e := range idx.Skills {
		if e.Hidden || !e.Eligible {
			continue
		}
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
	if action == "install" || action == "update" || action == "check_updates" {
		return t.skillGit(ctx, action, arguments)
	}
	if action == "propose_to_org" {
		return t.proposeToOrg(ctx, name, arguments)
	}
	if action == "configure" {
		return t.skillConfigure(name, arguments)
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

// skillGit relays a git-source action to the gateway's /v1/skill-git — the
// agent never runs git itself (R10.3); an install/update draft gets the
// same protected approval card as every other write.
func (t *Tools) skillGit(ctx context.Context, action string, arguments map[string]any) (any, error) {
	payload := map[string]string{"action": action}
	for _, key := range []string{"url", "ref", "path", "name", "grant"} {
		if v := argString(arguments, key); v != "" {
			payload[key] = v
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.CommunicationURL, "/")+"/v1/skill-git", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	request.Header.Set("X-Hub-Principal", os.Getenv("HUB_PRINCIPAL_ID"))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		slim, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("skill-git %s: %s: %s", action, response.Status, strings.TrimSpace(string(slim)))
	}
	var out struct {
		DraftID    string `json:"draft_id"`
		Name       string `json:"name"`
		ForcePush  bool   `json:"force_push"`
		Reports    []any  `json:"reports"`
		Candidates []any  `json:"candidates"`
	}
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		return nil, err
	}
	if action == "check_updates" {
		return map[string]any{"reports": out.Reports}, nil
	}
	if out.DraftID == "" {
		return map[string]any{"candidates": out.Candidates}, nil
	}
	link := t.skillDraftLink(ctx, map[string]string{
		"draft_id": out.DraftID, "skill_name": out.Name, "scope": "user",
		"summary":    argString(arguments, "reason"),
		"provenance": "git " + argString(arguments, "url"),
	})
	return map[string]any{
		"draft_id": out.DraftID, "name": out.Name, "state": "draft",
		"force_push": out.ForcePush,
		"link_sent":  link != "", "confirm_url": link,
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

// skillConfigure writes non-secret per-user settings under .state/settings
// (R8.4): private owner state like settings_set — no draft, no card. Only
// keys the effective index declares for that skill may be written.
func (t *Tools) skillConfigure(name string, arguments map[string]any) (any, error) {
	idx, err := t.effectiveIndex()
	if err != nil {
		return nil, err
	}
	declared := map[string]bool{}
	found := false
	for _, e := range idx.Skills {
		if e.Name != name {
			continue
		}
		found = true
		for _, s := range e.Settings {
			declared[s.Key] = true
		}
	}
	if !found {
		return nil, errSkillNotFound
	}
	raw, _ := arguments["values"].(map[string]any)
	values := map[string]string{}
	for key, v := range raw {
		if !declared[key] {
			return nil, fmt.Errorf("%w: setting %q is not declared by skill %s", skillstore.ErrInvalid, key, name)
		}
		value, _ := v.(string)
		values[key] = value
	}
	if err := skillstore.WriteSettings(t.SkillsStore, name, values); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "stored": true, "keys": len(values)}, nil
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
	idx, err := t.effectiveIndex()
	if err != nil {
		return nil, err
	}
	var entry *skillstore.Entry
	for i := range idx.Skills {
		if idx.Skills[i].Name == name {
			entry = &idx.Skills[i]
		}
	}
	if entry == nil {
		return nil, errSkillNotFound
	}
	if !entry.Eligible {
		return nil, fmt.Errorf("%w: skill %q is ineligible, missing %s",
			skillstore.ErrInvalid, name, strings.Join(entry.Missing, ", "))
	}
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
	result := map[string]any{
		"name":      name,
		"skill_dir": "/skills/" + name,
		"body":      body,
		"resources": resources,
		// R7.5: scripts ride only the caller's grants — without an exec
		// grant the skill is instructions only.
		"readiness": map[string]any{
			"missing":           entry.Missing,
			"deps_missing":      entry.DepsMissing,
			"has_scripts":       entry.HasScripts,
			"instructions_only": entry.InstructionsOnly,
			// R8.5: missing soft credentials are a setup-needed signal —
			// the skill stays invocable; hard ones already vetoed above.
			"setup_needed":        len(entry.CredsMissing) > 0,
			"credentials_missing": entry.CredsMissing,
		},
	}
	// R8.4: declared settings surface as effective per-user values —
	// the stored value wins, the declared default fills the rest.
	if len(entry.Settings) > 0 {
		stored, _ := skillstore.ReadSettings(t.SkillsStore, name)
		settings := map[string]string{}
		for _, spec := range entry.Settings {
			value := spec.Default
			if v, ok := stored[spec.Key]; ok {
				value = v
			}
			settings[spec.Key] = value
		}
		result["settings"] = settings
	}
	// R8.2: the protected credential form is the only path that stores
	// (principal, skill) secrets; the URL is delivery, never the value.
	if len(entry.CredsMissing) > 0 {
		if form, err := t.credentialForm(ctx, "skill:"+name, entry.CredsMissing); err == nil {
			if url, ok := form["form_url"].(string); ok {
				result["form_url"] = url
			}
		}
	}
	return result, nil
}
