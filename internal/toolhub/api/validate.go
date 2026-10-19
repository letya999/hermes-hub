package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
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
	if c.Schema != SchemaVersion || !identity.ValidID(c.ConnectionID) || !identity.ValidID(c.DefinitionID) || c.Revision == 0 || (c.Status != ActiveStatus && c.Status != DisabledStatus && c.Status != RevokedStatus && c.Status != DegradedStatus && c.Status != DeletingStatus) {
		return fmt.Errorf("%w: connection metadata", ErrInvalid)
	}
	if err := c.Owner.Validate(); err != nil {
		return err
	}
	if c.CredentialRefID != "" && !identity.ValidID(c.CredentialRefID) {
		return fmt.Errorf("%w: invalid credential reference", ErrInvalid)
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
	return nil
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
