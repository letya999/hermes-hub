package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

func (r CredentialReference) Validate() error {
	if r.Schema != SchemaVersion || !identity.ValidID(r.CredentialRefID) || !identity.ValidID(r.ConnectionID) || r.Revision == 0 || r.Backend == "" || r.Locator == "" || len(r.Keys) == 0 || (r.Status != ActiveStatus && r.Status != RevokedStatus) {
		return fmt.Errorf("%w: credential reference metadata", ErrInvalid)
	}
	if strings.ContainsAny(r.Locator, "\r\n=") || len(r.Locator) > 256 {
		return fmt.Errorf("%w: credential locator must be opaque metadata", ErrInvalid)
	}
	if r.Backend == "credential-broker" {
		if (r.BrokerGrantID != "" && !identity.ValidID(r.BrokerGrantID)) || !identity.ValidID(r.BrokerContractID) || r.BrokerContractRevision < 1 {
			return fmt.Errorf("%w: credential broker reference", ErrInvalid)
		}
		if r.BrokerContextID != "" && !identity.ValidID(r.BrokerContextID) {
			return fmt.Errorf("%w: credential broker context", ErrInvalid)
		}
		for key, target := range r.BrokerEnv {
			if !CredentialPattern.MatchString(key) || !CredentialPattern.MatchString(target) {
				return fmt.Errorf("%w: credential broker environment mapping", ErrInvalid)
			}
		}
	}
	seen := map[string]bool{}
	for _, key := range r.Keys {
		if !CredentialPattern.MatchString(key) || seen[key] {
			return fmt.Errorf("%w: invalid or duplicate credential key", ErrInvalid)
		}
		seen[key] = true
	}
	return nil
}

func CredentialReferenceID(connectionID string, revision uint64) string {
	return DeterministicID("cred", connectionID, fmt.Sprint(revision))
}

func (c Connection) Validate() error {
	if c.Schema != SchemaVersion || !identity.ValidID(c.ConnectionID) || !identity.ValidID(c.DefinitionID) || c.Revision == 0 || (c.Status != ActiveStatus && c.Status != DisabledStatus && c.Status != RevokedStatus && c.Status != DegradedStatus && c.Status != DeletingStatus && c.Status != ReauthStatus) {
		return fmt.Errorf("%w: connection metadata", ErrInvalid)
	}
	if err := c.Owner.Validate(); err != nil {
		return err
	}
	if c.CredentialRefID != "" && !identity.ValidID(c.CredentialRefID) {
		return fmt.Errorf("%w: invalid credential reference", ErrInvalid)
	}
	if c.CredentialVisibility != "" && c.CredentialVisibility != "active" && c.CredentialVisibility != "inactive" && c.CredentialVisibility != "unknown" {
		return fmt.Errorf("%w: credential visibility", ErrInvalid)
	}
	if c.CredentialVisibility != "" && c.CredentialCheckedAt.IsZero() {
		return fmt.Errorf("%w: credential check time", ErrInvalid)
	}
	if c.HealthCause != "" && c.HealthCause != "credential_visibility_unknown" && c.HealthCause != "credential_check_unavailable" && c.HealthCause != "credential_inactive" {
		return fmt.Errorf("%w: health cause", ErrInvalid)
	}
	if c.HealthCause != "" && c.CredentialVisibility == "" {
		return fmt.Errorf("%w: health cause without credential check", ErrInvalid)
	}
	if c.CredentialRetryCount < 0 || c.CredentialRetryCount > 5 || (c.CredentialRetryCount == 0 && !c.CredentialRetryAt.IsZero()) {
		return fmt.Errorf("%w: credential retry state", ErrInvalid)
	}
	if c.RecipeRetryCount < 0 || c.RecipeRetryCount > 5 || (c.RecipeRetryCount == 0 && !c.RecipeRetryAt.IsZero()) {
		return fmt.Errorf("%w: recipe retry state", ErrInvalid)
	}
	if c.CredRecipeRetryCount < 0 || c.CredRecipeRetryCount > 5 || (c.CredRecipeRetryCount == 0 && !c.CredRecipeRetryAt.IsZero()) {
		return fmt.Errorf("%w: credential recipe retry state", ErrInvalid)
	}
	if c.SurfaceRetryCount < 0 || c.SurfaceRetryCount > 5 || (c.SurfaceRetryCount == 0 && !c.SurfaceRetryAt.IsZero()) {
		return fmt.Errorf("%w: surface retry state", ErrInvalid)
	}
	if len(c.Metadata) > 32 {
		return fmt.Errorf("%w: too much connection metadata", ErrInvalid)
	}
	for key, value := range c.Metadata {
		if !regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`).MatchString(key) || len(value) > 256 || strings.ContainsAny(value, "\r\n") || strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "password") {
			return fmt.Errorf("%w: secret-like connection metadata is not allowed", ErrInvalid)
		}
	}
	if endpoint := c.Metadata["mcp_endpoint"]; endpoint != "" {
		if err := ValidateBackendEndpoint(endpoint); err != nil {
			return err
		}
	}
	for _, key := range []string{"egress_allow", "egress_deny"} {
		if _, err := ParseEgressList(c.Metadata[key]); err != nil {
			return err
		}
	}
	return nil
}

// ParseEgressList parses a comma-separated connection egress refinement:
// host[:port] names or CIDR prefixes, at most 32 entries, nothing empty.
func ParseEgressList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 32 {
		return nil, fmt.Errorf("%w: egress list is unbounded", ErrInvalid)
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		entry := strings.ToLower(strings.TrimSpace(part))
		if entry == "" {
			return nil, fmt.Errorf("%w: empty egress entry", ErrInvalid)
		}
		if _, err := netip.ParsePrefix(entry); err == nil {
			out = append(out, entry)
			continue
		}
		if !HostPattern.MatchString(entry) {
			return nil, fmt.Errorf("%w: invalid egress entry %q", ErrInvalid, entry)
		}
		out = append(out, entry)
	}
	return out, nil
}

func (b ToolBinding) Validate() error {
	if b.Schema != SchemaVersion || !identity.ValidID(b.ToolBindingID) || !identity.ValidID(b.PrincipalID) || !identity.ValidID(b.ContextID) || !identity.ValidID(b.RuntimeID) || !identity.ValidID(b.DefinitionID) || !VersionPattern.MatchString(b.DefinitionVersion) || !identity.ValidID(b.PolicyVersion) || b.Revision == 0 || b.ProjectionRevision == 0 {
		return fmt.Errorf("%w: binding identity or revision", ErrInvalid)
	}
	if b.WorkloadClass != Shared && b.WorkloadClass != PerUser && b.WorkloadClass != PerJob {
		return fmt.Errorf("%w: invalid binding workload class", ErrInvalid)
	}
	if b.Status != ActiveStatus && b.Status != DisabledStatus && b.Status != RevokedStatus {
		return fmt.Errorf("%w: invalid binding status", ErrInvalid)
	}
	if b.ConnectionID == "" && (b.ConnectionRevision != 0 || b.CredentialRefID != "" || b.CredentialRevision != 0) {
		return fmt.Errorf("%w: credential revision without connection", ErrInvalid)
	}
	if b.ConnectionID != "" && (!identity.ValidID(b.ConnectionID) || b.ConnectionRevision == 0) {
		return fmt.Errorf("%w: invalid binding connection", ErrInvalid)
	}
	if b.CredentialRefID != "" && (!identity.ValidID(b.CredentialRefID) || b.CredentialRevision == 0) {
		return fmt.Errorf("%w: invalid binding credential", ErrInvalid)
	}
	return nil
}

func DeterministicBindingID(principalID, contextID, runtimeID, definitionID, definitionVersion, connectionID, credentialRefID string) string {
	return DeterministicID("bind", principalID, contextID, runtimeID, definitionID, definitionVersion, connectionID, credentialRefID)
}

func ProjectedToolName(definitionID, version, toolName string) string {
	// The suffix disambiguates slug collisions between different definitions;
	// the version is deliberately outside it so a re-enable or upgrade keeps
	// the tool's public name instead of renaming it in every caller's surface.
	return "hub-" + slug(definitionID) + "-" + slug(toolName) + "-" + shortHash(definitionID, toolName)
}

func (W WorkloadInstance) Validate() error {
	if W.Schema != SchemaVersion || !identity.ValidID(W.WorkloadID) || !identity.ValidID(W.BindingID) || !identity.ValidID(W.DefinitionID) || !VersionPattern.MatchString(W.DefinitionVersion) || W.Generation == 0 {
		return fmt.Errorf("%w: workload identity", ErrInvalid)
	}
	if W.Class != Shared && W.Class != PerUser && W.Class != PerJob {
		return fmt.Errorf("%w: workload class", ErrInvalid)
	}
	if W.Status != StartingStatus && W.Status != RunningStatus && W.Status != StoppedStatus && W.Status != FailedStatus && W.Status != ExpiredStatus {
		return fmt.Errorf("%w: workload status", ErrInvalid)
	}
	switch W.Class {
	case Shared:
		if W.Owner != nil || W.RuntimeID != "" || W.JobID != "" {
			return fmt.Errorf("%w: shared workload has an owner or runtime", ErrInvalid)
		}
	case PerUser:
		if W.Owner == nil || W.Owner.Type != ContextOwner || !identity.ValidID(W.Owner.ID) || !identity.ValidID(W.RuntimeID) || W.JobID != "" {
			return fmt.Errorf("%w: per-user workload identity", ErrInvalid)
		}
	case PerJob:
		if W.Owner == nil || W.Owner.Type != ContextOwner || !identity.ValidID(W.Owner.ID) || !identity.ValidID(W.RuntimeID) || !identity.ValidID(W.JobID) || W.ExpiresAt.IsZero() {
			return fmt.Errorf("%w: per-job workload needs owner, job and expiry", ErrInvalid)
		}
	}
	return nil
}

func WorkloadInstanceID(definitionID string, class WorkloadClass, ownerID, jobID string) string {
	return DeterministicID("work", definitionID, string(class), ownerID, jobID)
}

func NewWorkloadInstance(binding ToolBinding, owner *OwnerRef, jobID string, generation uint64, startedAt, expiresAt time.Time) (WorkloadInstance, error) {
	W := WorkloadInstance{Schema: SchemaVersion, BindingID: binding.ToolBindingID, DefinitionID: binding.DefinitionID, DefinitionVersion: binding.DefinitionVersion, Class: binding.WorkloadClass, Owner: owner, JobID: jobID, Generation: generation, Status: StartingStatus, StartedAt: startedAt, ExpiresAt: expiresAt}
	if owner != nil {
		W.RuntimeID = binding.RuntimeID
	}
	ownerID := ""
	if owner != nil {
		ownerID = owner.ID
	}
	if W.Class == PerUser && binding.ConnectionID != "" {
		ownerID += ":" + binding.PrincipalID + ":" + binding.ConnectionID
	}
	if W.Class == PerUser && binding.ConnectionID == "" {
		ownerID = binding.ToolBindingID
	}
	W.WorkloadID = WorkloadInstanceID(W.DefinitionID, W.Class, ownerID, jobID)
	return W, W.Validate()
}

func DefinitionsEqual(a, b ToolDefinition) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func RecordsEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func DeterministicID(prefix string, values ...string) string {
	h := sha256.New()
	for _, value := range values {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))[:24]
}

func shortHash(values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(digest[:])[:8]
}

func slug(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "tool"
	}
	if len(result) > 20 {
		return result[:20]
	}
	return result
}
