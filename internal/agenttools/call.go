package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path"
	"strings"

	"github.com/letya999/hermes-hub/internal/media"
)

// Call runs one tool through the same dispatch the ToolHub executor uses.
// Server() closures delegate here so the direct MCP surface and the
// capability-admitted path can never diverge on semantics.
func (t *Tools) Call(ctx context.Context, name string, r Input) (any, error) {
	return t.call(ctx, name, r, t.Workspace, t.Archive, t.Organization)
}

// ExecCall runs one ToolHub-admitted call. arguments were already validated
// against the tool's admitted input schema by the gateway; scopes are the
// verbatim capability path boundaries bound by admission. This is the
// private executor entry point — never an MCP surface.
func (t *Tools) ExecCall(ctx context.Context, name string, arguments map[string]any, scopes []api.CapabilityScope) (any, error) {
	body, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("executor arguments: %w", err)
	}
	var r Input
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("executor arguments: %w", err)
	}
	// Private scratch exports must survive JSON without UTF-8 replacement.
	if encoded, present := arguments["export_base64"]; present {
		value, ok := encoded.(string)
		if name != "file_write" || !ok || len(value) > base64.StdEncoding.EncodedLen(Limit) || r.Text != "" || r.Revision != "" {
			return nil, errors.New("invalid private file export")
		}
		r.exportBytes, err = base64.StdEncoding.DecodeString(value)
		if err != nil || len(r.exportBytes) > Limit {
			return nil, errors.New("invalid private file export")
		}
	}
	if name == "image_inspect" && len(r.Paths) > 0 {
		return nil, errors.New("use images_inspect for capability-admitted image sets")
	}
	ws, ar, org, done, err := t.scopedRoots(name, &r, scopes)
	if err != nil {
		return nil, err
	}
	defer done()
	return t.call(ctx, name, r, ws, ar, org)
}

// call dispatches to one handler. ws, ar and org are the call-scoped roots;
// handlers for non-filesystem capabilities ignore them.
func (t *Tools) call(ctx context.Context, name string, r Input, ws, ar, org *os.Root) (any, error) {
	if reason := t.toolPolicyDeny(name); reason != "" {
		return nil, errors.New(reason)
	}
	if t.ReadOnlyTools[name] {
		return nil, fmt.Errorf("%s is read-only on this runtime", name)
	}
	session := func() *media.Session {
		if ws == t.Workspace {
			return t.session()
		}
		return media.New(ws)
	}
	switch name {
	case "file_list":
		return t.fileScoped("list", r, ws, ar, org)
	case "file_read":
		return t.fileScoped("read", r, ws, ar, org)
	case "file_write":
		return t.fileScoped("write", r, ws, ar, org)
	case "file_search":
		return t.fileScoped("search", r, ws, ar, org)
	case "service_catalog":
		return t.ServiceCatalog()
	case "service_enable":
		return t.ServiceEnable(r)
	case "service_disable":
		return t.ServiceDisable(r)
	case "settings_get":
		return t.SettingsGet(r)
	case "settings_set":
		return t.SettingsSet(r)
	case "routine_create", "routine_list", "routine_update", "routine_pause", "routine_snooze", "routine_delete":
		return t.Routine(ctx, name, r)
	case "document_extract":
		return session().Extract(ctx, r.Path)
	case "document_create":
		return session().Create(ctx, r.Name, r.Format, r.Text)
	case "document_edit":
		return session().EditDocument(ctx, r.Path, r.Text)
	case "document_convert":
		return session().ConvertDocument(ctx, r.Path, r.Name, r.Format)
	case "archive_create":
		return t.archiveCreate(ws, r)
	case "archive_unpack":
		return t.archiveUnpack(ws, r)
	case "inbox_fetch":
		return t.inboxFetch(ctx, ws, r)
	case "knowledge_list":
		return t.knowledgeList(ctx)
	case "knowledge_search":
		return t.knowledgeSearch(ctx, r.Query)
	case "knowledge_read":
		return t.knowledgeRead(ctx, r)
	case "image_inspect", "images_inspect":
		if len(r.Paths) > 0 {
			return session().InspectMany(ctx, r.Paths)
		}
		return session().Inspect(ctx, r.Path)
	case "image_convert":
		return session().ConvertImage(ctx, r.Name, r.Path, r.Format)
	case "artifact_remove":
		return session().Remove(r.Path)
	case "image_generate":
		if !session().ImageGranted() {
			return nil, errors.New("image generation is not granted on this runtime")
		}
		return session().Generate(ctx, r.Name, r.Prompt)
	case "image_edit":
		if !session().ImageGranted() {
			return nil, errors.New("image editing is not granted on this runtime")
		}
		return session().Edit(ctx, r.Name, r.Path, r.Prompt)
	case "video_generate":
		if !session().ImageGranted() {
			return nil, errors.New("video generation is not granted on this runtime")
		}
		return session().VideoGenerate(ctx, r.Prompt)
	case "media_fetch":
		if !session().ImageGranted() {
			return nil, errors.New("media fetch is not granted on this runtime")
		}
		return session().MediaFetch(ctx, r.ID, r.Name)
	case "hh_search", "hh_vacancy", "hh_resumes", "hh_apply":
		return t.API(ctx, name, r)
	case "ssh_hosts", "ssh_exec", "ssh_read", "ssh_write", "ssh_shell_open", "ssh_shell_send", "ssh_shell_read", "ssh_shell_close", "ssh_tunnel_open", "ssh_tunnel_list", "ssh_tunnel_close":
		return t.sshDispatch(ctx, name, r)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

// toolPolicyDeny re-checks the rendered governance extract at dispatch — the
// plane-3 fence that keeps a mid-session denial effective even for a binding
// whose scopes were admitted before the rule landed. Capability families are
// the snapshot's unit; a write-effect tool of a read-only family refuses
// exactly like HUB_TOOLS_RO.
func (t *Tools) toolPolicyDeny(name string) string {
	p := t.ToolPolicy
	if p == nil {
		return ""
	}
	if p.DenyAll {
		return name + " is denied by tool governance on this runtime"
	}
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name != name {
			continue
		}
		family, _, _ := strings.Cut(spec.CapabilityID, ".")
		if p.Denied(spec.CapabilityID) || p.Denied(family) {
			return name + " is denied by tool governance on this runtime"
		}
		if spec.Effect == api.WriteEffect && (p.CappedReadOnly(spec.CapabilityID) || p.CappedReadOnly(family)) {
			return name + " is read-only on this runtime"
		}
		return ""
	}
	return ""
}

func (t *Tools) sshDispatch(ctx context.Context, name string, r Input) (any, error) {
	svc, err := t.sshService()
	if err != nil {
		return nil, err
	}
	switch name {
	case "ssh_hosts":
		return map[string]any{"hosts": svc.Hosts()}, nil
	case "ssh_exec":
		return svc.Exec(ctx, r.Host, r.Command)
	case "ssh_read":
		return svc.Read(ctx, r.Host, r.Path)
	case "ssh_write":
		if !t.SSHGrants.Write {
			return nil, errors.New("ssh_write grant is not active")
		}
		return svc.Write(ctx, r.Host, r.Path, r.Text)
	case "ssh_shell_open":
		if !t.SSHGrants.Shell {
			return nil, errors.New("ssh_shell grant is not active")
		}
		return svc.ShellOpen(ctx, r.Host)
	case "ssh_shell_send":
		if !t.SSHGrants.Shell {
			return nil, errors.New("ssh_shell grant is not active")
		}
		return map[string]any{"id": r.ID, "sent": len(r.Data)}, svc.ShellSend(r.ID, r.Data)
	case "ssh_shell_read":
		if !t.SSHGrants.Shell {
			return nil, errors.New("ssh_shell grant is not active")
		}
		return svc.ShellRead(r.ID, r.Offset)
	case "ssh_shell_close":
		if !t.SSHGrants.Shell {
			return nil, errors.New("ssh_shell grant is not active")
		}
		return map[string]any{"id": r.ID, "closed": true}, svc.ShellClose(r.ID)
	case "ssh_tunnel_open":
		if !t.SSHGrants.Tunnel {
			return nil, errors.New("ssh_tunnel grant is not active")
		}
		return svc.TunnelOpen(ctx, r.Host, r.Name)
	case "ssh_tunnel_list":
		if !t.SSHGrants.Tunnel {
			return nil, errors.New("ssh_tunnel grant is not active")
		}
		return map[string]any{"tunnels": svc.TunnelList()}, nil
	case "ssh_tunnel_close":
		if !t.SSHGrants.Tunnel {
			return nil, errors.New("ssh_tunnel grant is not active")
		}
		return map[string]any{"id": r.ID, "closed": true}, svc.TunnelClose(r.ID)
	}
	return nil, fmt.Errorf("unknown ssh operation %q", name)
}

// scopedRoots narrows the executor's roots to the capability boundaries the
// admission bound for this call and rewrites the scoped path arguments to
// their sub-root relative form. The ToolHub backend passes the admitted
// scopes verbatim; this routine only enforces them mechanically and can
// never widen a grant.
func (t *Tools) scopedRoots(name string, r *Input, scopes []api.CapabilityScope) (ws, ar, org *os.Root, done func(), err error) {
	ws, ar, org = t.Workspace, t.Archive, t.Organization
	opened := []*os.Root{}
	done = func() {
		for _, root := range opened {
			_ = root.Close()
		}
	}
	seenScopes := map[string]bool{}
	argumentPrefixes := map[string]string{}
	for _, scope := range scopes {
		if scope.PathPrefix == "" {
			continue // Whole-domain grant: no narrower boundary to enforce.
		}
		if scope.PathArgument != "path" && scope.PathArgument != "paths" {
			done()
			return nil, nil, nil, done, fmt.Errorf("capability scope on unmapped argument %q", scope.PathArgument)
		}
		// Read and write uses on the same path argument admit identical
		// boundaries; applying one twice would double-rewrite the argument.
		key := scope.Resource + "\x00" + scope.PathArgument + "\x00" + scope.PathPrefix
		if seenScopes[key] {
			continue
		}
		seenScopes[key] = true
		if previous, exists := argumentPrefixes[scope.PathArgument]; exists && previous != scope.PathPrefix {
			done()
			return nil, nil, nil, done, errors.New("multi-file calls require one common admitted directory; use separate calls for different grants")
		}
		argumentPrefixes[scope.PathArgument] = scope.PathPrefix
		base, err := t.scopeBase(scope.Resource, r)
		if err != nil {
			done()
			return nil, nil, nil, done, err
		}
		actual := r.Path
		if scope.PathArgument == "paths" {
			if len(r.Paths) == 0 {
				done()
				return nil, nil, nil, done, errors.New("paths required")
			}
			actual = r.Paths[0]
		}
		sub, rel, err := ScopedSub(base, scope.PathPrefix, actual)
		if err != nil {
			done()
			return nil, nil, nil, done, fmt.Errorf("capability scope %q: %w", scope.PathPrefix, err)
		}
		opened = append(opened, sub)
		if scope.PathArgument == "paths" {
			dir := scope.PathPrefix
			if actual == dir && rel != "" {
				dir = path.Dir(dir)
			}
			original := append([]string(nil), r.Paths...)
			for i, item := range r.Paths {
				if item != scope.PathPrefix && !strings.HasPrefix(item, scope.PathPrefix+"/") {
					done()
					return nil, nil, nil, done, errors.New("path outside admitted scope")
				}
				r.Paths[i] = strings.TrimPrefix(strings.TrimPrefix(item, dir), "/")
			}
			if name == "archive_create" {
				r.archiveSources = sub
				r.archiveSourceNames = original
				continue
			}
		}
		if name == "archive_create" && scope.PathArgument == "path" {
			r.archiveOutput = sub
			r.archiveOutputPath = rel
			continue
		}

		switch scope.Resource {
		case "files":
			switch strings.ToLower(r.Root) {
			case "", "workspace":
				ws = sub
			case "archive":
				ar = sub
			case "organization":
				org = sub
			}
		default:
			ws = sub
		}
		if scope.PathArgument == "path" {
			r.Path = rel
		}
	}
	return ws, ar, org, done, nil
}

// ScopedSub opens the sub-root an admitted path boundary selects and returns
// the path relative to it. A file-level boundary (actual == prefix) scopes to
// the containing directory so the target itself stays reachable; a directory
// boundary scopes inside it. Callers must handle the whole-domain case
// (empty prefix) themselves.
func ScopedSub(base *os.Root, prefix, actual string) (*os.Root, string, error) {
	if !safe(prefix) || (actual != "" && (!safe(actual) || (actual != prefix && !strings.HasPrefix(actual, prefix+"/")))) {
		return nil, "", errors.New("path outside capability scope")
	}
	dir := prefix
	if actual == prefix {
		target, err := base.Lstat(prefix)
		if err != nil && !os.IsNotExist(err) {
			return nil, "", err
		}
		if err == nil && target.Mode()&os.ModeSymlink != 0 {
			return nil, "", errors.New("capability target must not be a symlink")
		}
		if err != nil || !target.IsDir() {
			dir = path.Dir(prefix)
		}
	}
	info, err := base.Lstat(dir)
	if err != nil {
		return nil, "", err
	}
	current := ""
	for _, part := range strings.Split(dir, "/") {
		current = path.Join(current, part)
		component, err := base.Lstat(current)
		if err != nil {
			return nil, "", err
		}
		if !component.IsDir() || component.Mode()&os.ModeSymlink != 0 {
			return nil, "", errors.New("capability scope must not traverse a symlink")
		}
	}
	sub, err := base.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	opened, err := sub.Lstat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = sub.Close()
		return nil, "", errors.New("capability scope changed while opening")
	}
	rel := strings.TrimPrefix(actual, dir)
	return sub, strings.TrimPrefix(rel, "/"), nil
}

// scopeBase resolves the resource domain to the base root a scope narrows.
// "files" follows the root argument of the file tool; document, image and
// artifact scopes always land on the workspace root.
func (t *Tools) scopeBase(resource string, r *Input) (*os.Root, error) {
	switch resource {
	case "files":
		return t.root(r.Root)
	case "documents", "images", "artifacts", "workspace":
		return t.Workspace, nil
	case "archive":
		return t.Archive, nil
	case "organization":
		if t.Organization == nil {
			return nil, errors.New("organization root is not configured")
		}
		return t.Organization, nil
	}
	return nil, fmt.Errorf("unknown capability resource %q", resource)
}
