package api

import (
	"fmt"
	"strings"
)

// Controller-owned CLI sandbox cells. A bounded-cli call no longer execs
// inside the ToolHub container; the controller creates a sibling container —
// the cell — that carries exactly one workspace bind, the read-only cellinit
// helper, no host state, no broker material and no docker socket. Warm tiers
// reuse cells behind a re-inspect + canary guard; anything that drifts is
// killed and audited, never reused.

// CliWorkspace is the single bind a cell may carry. Scope none mounts
// nothing (the cell gets a scratch tmpfs at /work); binding binds the
// controller-local path the ToolHub resolved through OpenWorkloadWorkspace;
// principal binds <spaces_root>/<principal>/workspace resolved here — or
// <spaces_root>/<principal>/managed/<environment>/workspace when a managed
// deployment tier is configured and that directory exists.
type CliWorkspace struct {
	Scope  string `json:"scope"`
	Path   string `json:"path,omitempty"`
	Access string `json:"access,omitempty"`
}

// CliBrokeredCred is one credential delivered through the cell-local
// cred-proxy sidecar: the cell env carries only
// http://<proxy>/<host> and the proxy injects the real header on the
// reviewed upstream. The value crosses to the controller in the request
// body — over the bearer-authenticated control channel — but never enters
// container config, argv or logs.
type CliBrokeredCred struct {
	EnvName string `json:"env_name"`
	Host    string `json:"host"`
	Prefix  string `json:"prefix,omitempty"`
	Value   string `json:"value"`
}

// ValidCellImageRef accepts the three refs a cell image may take: an
// image@sha256 digest pin, a local image ID, or a local name[:tag] the
// daemon resolves to an image ID at create time. Toolbox members stay
// digest-pinned only — caller-controlled surfaces never take a bare tag.
func ValidCellImageRef(ref string) error {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		if at == 0 || !DigestPattern.MatchString(ref[at+1:]) || strings.ContainsAny(ref[:at], " \t\r\n") || strings.Contains(ref, "..") {
			return fmt.Errorf("%w: cell image %q must be digest-pinned", ErrInvalid, ref)
		}
		return nil
	}
	if DockerImageIDPattern.MatchString(ref) {
		return nil
	}
	if err := ValidateLocalImageName(ref); err != nil {
		return fmt.Errorf("%w: cell image %q", ErrInvalid, ref)
	}
	return nil
}

// ValidPinnedImageRef is the caller-visible form: toolbox members and
// artifact plans must pin an image@sha256 manifest digest; daemon-local
// tags would let the approved surface drift silently.
func ValidPinnedImageRef(ref string) error {
	at := strings.LastIndex(ref, "@")
	if at <= 0 || !DigestPattern.MatchString(ref[at+1:]) || strings.ContainsAny(ref[:at], " \t\r\n") || strings.Contains(ref, "..") {
		return fmt.Errorf("%w: cell image %q must be digest-pinned", ErrInvalid, ref)
	}
	return nil
}
