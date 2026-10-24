package toolhub

import (
	"net/url"
	"regexp"
	"strings"
)

// A connector backend may print its own interactive-OAuth handoff: either a
// provider authorize URL whose redirect_uri is a container-local loopback
// (http://localhost:8000/oauth2callback) or the bare loopback callback itself.
// Neither is reachable from a user browser — the callback lives inside the
// workload container — so the caller is told to use managed onboarding instead
// of being handed a dead link.
var toolOutputURL = regexp.MustCompile(`https?://[^\s"'<>\)\]]+`)

const loopbackOAuthPlaceholder = "[internal OAuth link withheld — sign-in runs through the managed connector setup page]"

func scrubLoopbackOAuthURL(text string) string {
	return toolOutputURL.ReplaceAllStringFunc(text, func(raw string) string {
		if loopbackOAuthURL(raw) {
			return loopbackOAuthPlaceholder
		}
		return raw
	})
}

func loopbackOAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	if loopbackHost(u.Hostname()) && u.Path == "/oauth2callback" {
		return true
	}
	redirect := u.Query().Get("redirect_uri")
	if redirect == "" {
		return false
	}
	target, err := url.Parse(redirect)
	return err == nil && loopbackHost(target.Hostname()) && target.Path == "/oauth2callback"
}

func loopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]", "host.docker.internal":
		return true
	}
	return false
}
