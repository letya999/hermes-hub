package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ReconnectMarkerName = "toolhub-reconnect.request"

type ProjectionChange struct {
	Revision uint64 `json:"revision"`
	Reason   string `json:"reason"`
}

// WriteReconnectMarker records a projection change for the runtime watcher.
// The watcher restarts Hermes from its persistent owner home when needed.
func WriteReconnectMarker(stateDir string, change ProjectionChange) error {
	if strings.TrimSpace(stateDir) == "" || change.Revision == 0 {
		return fmt.Errorf("%w: reconnect marker", ErrInvalid)
	}
	if err := NoSymlinkPath(stateDir); err != nil {
		return err
	}
	info, err := os.Stat(stateDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: reconnect marker directory", ErrInvalid)
	}
	body, err := json.Marshal(change)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, ReconnectMarkerName)
	tmp, err := os.CreateTemp(stateDir, ".toolhub-reconnect-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Projection exclusion reasons are the SPEC-0053 R1.3 vocabulary: a binding
// that drops out of a projection must surface with exactly one of these.
const (
	ExcludePolicyMismatch = "policy_mismatch"
	ExcludeDisabled       = "disabled"
	ExcludeRevoked        = "revoked"
	ExcludeStale          = "stale"
	ExcludeDegraded       = "degraded"
	ExcludeNotFound       = "not_found"
)

// ProjectionExclusion names one binding that belongs to a runtime projection
// but failed admission, with the reason. Logged once per
// (principal, binding, projection revision, reason) and exposed to the
// diagnostics surface scoped to the requesting envelope.
type ProjectionExclusion struct {
	PrincipalID        string `json:"principal_id"`
	ContextID          string `json:"context_id"`
	RuntimeID          string `json:"runtime_id"`
	BindingID          string `json:"binding_id"`
	Reason             string `json:"reason"`
	ProjectionRevision uint64 `json:"projection_revision"`
}

// ProjectedTool is the server-side mapping exposed to an authenticated Hermes
// runtime. BindingID is metadata for the gateway; it is never accepted from a
// model tool argument.
type ProjectedTool struct {
	Name         string
	BindingID    string
	DefinitionID string
	Version      string
	Tool         ToolSpec
}

// ClassifyProjectionExclusion maps a resolveLocked failure to the SPEC-0053
// R1.3 reason vocabulary.
func ClassifyProjectionExclusion(binding ToolBinding, err error) string {
	switch binding.Status {
	case RevokedStatus:
		return ExcludeRevoked
	case DisabledStatus:
		return ExcludeDisabled
	}
	switch {
	case errors.Is(err, ErrRevoked):
		return ExcludeRevoked
	case errors.Is(err, ErrDegraded):
		return ExcludeDegraded
	case errors.Is(err, ErrStale):
		return ExcludeStale
	default:
		return ExcludePolicyMismatch
	}
}

// ExclusionLogLimit bounds the in-memory exclusion set; the oldest entries are
// evicted first. Exclusions are observability, not authority — the persisted
// registry stays the source of truth.
const ExclusionLogLimit = 256

func ValidateProjectedName(projectedName string) error {
	if projectedName == "" || len(projectedName) > 128 || !toolNamePattern.MatchString(projectedName) || strings.ContainsAny(projectedName, "\r\n") {
		return fmt.Errorf("%w: projected tool name", ErrInvalid)
	}
	return nil
}
