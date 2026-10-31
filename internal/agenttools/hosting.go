package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/hosting"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (t *Tools) hostingCall(ctx context.Context, name string, r Input, workspace *os.Root) (any, error) {
	base := os.Getenv("HUB_HOSTING_URL")
	token := os.Getenv("HUB_HOSTING_TOKEN")
	if token == "" {
		if p := os.Getenv("HUB_HOSTING_TOKEN_FILE"); p != "" {
			var err error
			token, err = hosting.ReadControlToken(p)
			if err != nil {
				return nil, hosting.ErrUnavailable
			}
		}
	}
	if !hosting.PrivateEndpoint(base, "8182", "") || len(token) < 32 {
		return nil, hosting.ErrUnavailable
	}
	var body any
	endpoint := ""
	switch name {
	case "site_list":
		endpoint = "/v1/list"
		body = struct{}{}
	case "site_status":
		endpoint = "/v1/status"
		body = struct {
			Name string `json:"name"`
		}{r.Name}
	case "site_unpublish":
		endpoint = "/v1/unpublish"
		body = hosting.UnpublishRequest{Name: r.Name, ExpectedRevision: r.ExpectedRevision, RequestID: r.RequestID}
	case "site_publish":
		if workspace == nil || t.Workspace == nil || r.Access == "public" && !r.Authorized {
			return nil, hosting.ErrDenied
		}
		prefix, err := filepath.Rel(t.Workspace.Name(), workspace.Name())
		if err != nil || !filepath.IsLocal(prefix) {
			return nil, hosting.ErrDenied
		}
		if !filepath.IsLocal(r.Path) || path.Clean(r.Path) != r.Path || strings.ContainsAny(r.Path, "\\:\x00") {
			return nil, hosting.ErrDenied
		}
		source := r.Path
		if prefix != "." {
			source = path.Join(filepath.ToSlash(prefix), source)
		}
		body = hosting.PublishRequest{Name: r.Name, Source: source, Mode: r.Mode, Access: r.Access, Profile: r.Profile, ExpectedRevision: r.ExpectedRevision, RequestID: r.RequestID, LifetimeSeconds: r.LifetimeSeconds}
		endpoint = "/v1/publish"
	default:
		return nil, hosting.ErrInvalid
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, hosting.ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+endpoint, bytes.NewReader(payload)) // #nosec G704 -- operator-only internal URL is allowlisted by PrivateEndpoint; tool args cannot choose a destination.
	if err != nil {
		return nil, hosting.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := hosting.PrivateClient(t.HTTP).Do(req) // #nosec G704 -- allowlisted internal endpoint; no ambient proxy, cookie jar or redirects.
	if err != nil {
		return nil, hosting.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		switch response.StatusCode {
		case 400:
			return nil, hosting.ErrInvalid
		case 403:
			return nil, hosting.ErrDenied
		case 404:
			return nil, hosting.ErrNotFound
		case 409:
			return nil, hosting.ErrConflict
		case 429:
			return nil, hosting.ErrQuota
		}
		return nil, hosting.ErrUnavailable
	}
	var out any
	d := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF {
		return nil, hosting.ErrUnavailable
	}
	return out, nil
}
func (t *Tools) registerHosting(server *mcp.Server) {
	if os.Getenv("HUB_HOSTING_URL") == "" {
		return
	}
	for _, name := range []string{"site_publish", "site_unpublish", "site_list", "site_status"} {
		mcp.AddTool(server, &mcp.Tool{Name: name, Description: "Manage one owner-bound hosted site. Publish prebuilt workspace files; URLs are returned only when ready. Public access requires explicit authorization."}, func(ctx context.Context, _ *mcp.CallToolRequest, r Input) (*mcp.CallToolResult, any, error) {
			out, err := t.hostingCall(ctx, name, r, t.Workspace)
			return nil, out, err
		})
	}
}
