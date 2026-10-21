package api

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// OAuthRecipe is a reviewed local MCP authentication contract.
type ConnectionRecipeContextKey struct{}

type OAuthRecipe struct {
	AccountInput               string         `json:"account_input,omitempty"`
	AccessType                 string         `json:"access_type,omitempty"`
	Prompt                     string         `json:"prompt,omitempty"`
	AuthorizeURL               string         `json:"authorize_url"`
	TokenURL                   string         `json:"token_url"`
	Scopes                     []string       `json:"scopes"`
	ClientInput                string         `json:"client_input"`
	ClientSections             []string       `json:"client_sections,omitempty"`
	RegisteredRedirectSections []string       `json:"registered_redirect_sections,omitempty"`
	StateTarget                string         `json:"state_target"`
	TokenFile                  string         `json:"token_file"`
	TokenTemplate              map[string]any `json:"token_template"`
	ProbeTool                  string         `json:"probe_tool"`
	ProbeArgs                  map[string]any `json:"probe_args,omitempty"`
}

func (a OAuthRecipe) OAuthClient(data []byte, redirect string) (string, string, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return "", "", ErrInvalid
	}
	for _, section := range a.ClientSections {
		client, _ := document[section].(map[string]any)
		id, _ := client["client_id"].(string)
		secret, _ := client["client_secret"].(string)
		if id != "" && secret != "" {
			if slices.Contains(a.RegisteredRedirectSections, section) {
				allowed := false
				uris, _ := client["redirect_uris"].([]any)
				for _, uri := range uris {
					allowed = allowed || uri == redirect
				}
				if !allowed {
					return "", "", fmt.Errorf("%w: OAuth callback is not registered", ErrInvalid)
				}
			}
			return id, secret, nil
		}
	}
	return "", "", ErrInvalid
}

func (a OAuthRecipe) RenderToken(values map[string]string, clientID, clientSecret string, now time.Time) ([]byte, error) {
	granted := strings.Fields(values["OAUTH_SCOPE"])
	for _, scope := range a.Scopes {
		if !slices.Contains(granted, scope) {
			return nil, ErrReconsent
		}
	}
	seconds, err := strconv.Atoi(values["EXPIRES_IN"])
	if err != nil || seconds <= 0 || int64(seconds) > int64((1<<63-1)/time.Second) {
		return nil, ErrInvalid
	}
	replacements := map[string]any{"$oauth.ACCESS_TOKEN": values["ACCESS_TOKEN"], "$oauth.REFRESH_TOKEN": values["REFRESH_TOKEN"], "$oauth.EXPIRES_AT_RFC3339": now.Add(time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339), "$oauth.EXPIRES_AT_UNIX_MS": now.Add(time.Duration(seconds) * time.Second).UnixMilli(), "$oauth.TOKEN_TYPE": values["TOKEN_TYPE"], "$oauth.OAUTH_SCOPE": values["OAUTH_SCOPE"], "$oauth.TOKEN_URL": a.TokenURL, "$oauth.SCOPES": granted, "$client.ID": clientID, "$client.SECRET": clientSecret}
	var render func(any) any
	render = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			result := map[string]any{}
			for k, item := range x {
				result[k] = render(item)
			}
			return result
		case string:
			if replacement, ok := replacements[x]; ok {
				return replacement
			}
		}
		return v
	}
	data, err := json.Marshal(render(a.TokenTemplate))
	if err != nil || len(data) > 65536 {
		return nil, ErrInvalid
	}
	return data, nil
}

func (a OAuthRecipe) Validate() error {
	invalid := func() error { return fmt.Errorf("%w: OAuth recipe", ErrInvalid) }
	for _, raw := range []string{a.AuthorizeURL, a.TokenURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return invalid()
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !RemoteAddrPublic(ip) {
			return invalid()
		}
		if strings.HasSuffix(strings.ToLower(u.Hostname()), ".internal") {
			return invalid()
		}
	}
	if len(a.Scopes) == 0 || a.TokenFile == ".." || path.Base(a.TokenFile) != a.TokenFile || strings.Contains(a.TokenFile, "\\") || !ValidContainerMountTarget(a.StateTarget) {
		return invalid()
	}
	var check func(any) bool
	check = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for _, item := range x {
				if !check(item) {
					return false
				}
			}
		case string:
			if strings.HasPrefix(x, "$") {
				switch x {
				case "$oauth.ACCESS_TOKEN", "$oauth.REFRESH_TOKEN", "$oauth.EXPIRES_AT_RFC3339", "$oauth.EXPIRES_AT_UNIX_MS", "$oauth.TOKEN_TYPE", "$oauth.OAUTH_SCOPE", "$oauth.TOKEN_URL", "$oauth.SCOPES", "$client.ID", "$client.SECRET":
				default:
					return false
				}
			}
		case []any:
			return false
		}
		return true
	}
	data, err := json.Marshal(a.TokenTemplate)
	if err != nil || len(data) > 65536 || !check(a.TokenTemplate) {
		return invalid()
	}
	return nil
}
