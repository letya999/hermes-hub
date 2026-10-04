package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/toolhub"
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
func (t *Tools) ExecCall(ctx context.Context, name string, arguments map[string]any, scopes []toolhub.CapabilityScope) (any, error) {
	body, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("executor arguments: %w", err)
	}
	var r Input
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("executor arguments: %w", err)
	}
	ws, ar, org, done, err := t.scopedRoots(&r, scopes)
	if err != nil {
		return nil, err
	}
	defer done()
	return t.call(ctx, name, r, ws, ar, org)
}

// call dispatches to one handler. ws, ar and org are the call-scoped roots;
// handlers for non-filesystem capabilities ignore them.
func (t *Tools) call(ctx context.Context, name string, r Input, ws, ar, org *os.Root) (any, error) {
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
	case "routine_create", "routine_list", "routine_update", "routine_pause", "routine_delete":
		return t.Routine(ctx, name, r)
	case "document_extract":
		return session().Extract(ctx, r.Path)
	case "document_create":
		return session().Create(ctx, r.Name, r.Format, r.Text)
	case "document_edit":
		return session().EditDocument(ctx, r.Path, r.Text)
	case "document_convert":
		return session().ConvertDocument(ctx, r.Path, r.Name, r.Format)
	case "image_inspect":
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
	case "hh_search", "hh_vacancy", "hh_resumes", "hh_apply":
		return t.API(ctx, name, r)
	case "ssh_hosts", "ssh_exec", "ssh_read", "ssh_write", "ssh_shell_open", "ssh_shell_send", "ssh_shell_read", "ssh_shell_close", "ssh_tunnel_open", "ssh_tunnel_list", "ssh_tunnel_close":
		return t.sshDispatch(ctx, name, r)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
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
func (t *Tools) scopedRoots(r *Input, scopes []toolhub.CapabilityScope) (ws, ar, org *os.Root, done func(), err error) {
	ws, ar, org = t.Workspace, t.Archive, t.Organization
	opened := []*os.Root{}
	done = func() {
		for _, root := range opened {
			_ = root.Close()
		}
	}
	seenScopes := map[string]bool{}
	for _, scope := range scopes {
		if scope.PathPrefix == "" {
			continue // Whole-domain grant: no narrower boundary to enforce.
		}
		if scope.PathArgument != "path" {
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
		base, err := t.scopeBase(scope.Resource, r)
		if err != nil {
			done()
			return nil, nil, nil, done, err
		}
		sub, rel, err := ScopedSub(base, scope.PathPrefix, r.Path)
		if err != nil {
			done()
			return nil, nil, nil, done, fmt.Errorf("capability scope %q: %w", scope.PathPrefix, err)
		}
		opened = append(opened, sub)
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
		r.Path = rel
	}
	return ws, ar, org, done, nil
}

// ScopedSub opens the sub-root an admitted path boundary selects and returns
// the path relative to it. A file-level boundary (actual == prefix) scopes to
// the containing directory so the target itself stays reachable; a directory
// boundary scopes inside it. Callers must handle the whole-domain case
// (empty prefix) themselves.
func ScopedSub(base *os.Root, prefix, actual string) (*os.Root, string, error) {
	dir := prefix
	if actual == prefix {
		dir = path.Dir(prefix)
	}
	sub, err := base.OpenRoot(dir)
	if err != nil {
		return nil, "", err
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
