package stack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// EnrollSiblingRuntimeTokens writes each sibling space's HUB_RUNTIME_AUTH into
// this infra owner's toolhub-tokens.json. The shared ToolHub accepts those
// bearers as separate identities. The owner's own token stays the primary env
// token and is never copied into the file.
//
// The parent directory must be named "spaces". Render tests whose parent is a
// temp directory no-op instead of scanning unrelated folders.
//
// The file is updated in place. A Docker bind mount follows the inode, so a
// rename would hide the new contents from a running ToolHub.
func EnrollSiblingRuntimeTokens(spaceDir, environment string) error {
	spaceDir, err := filepath.Abs(spaceDir)
	if err != nil {
		return err
	}
	parent := filepath.Dir(spaceDir)
	if filepath.Base(parent) != "spaces" {
		return nil
	}
	owner, err := ReadEnvironment(spaceDir, environment)
	if err != nil {
		return err
	}
	ownerToken := runtimeAuthToken(filepath.Join(spaceDir, "runtime.auth"))
	path := filepath.Join(spaceDir, "toolhub-tokens.json")
	entries, err := readTokenFile(path)
	if err != nil {
		return err
	}
	siblings, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, sibling := range siblings {
		if !sibling.IsDir() {
			continue
		}
		sibDir := filepath.Join(parent, sibling.Name())
		if sameDir(sibDir, spaceDir) {
			continue
		}
		settings, readErr := ReadEnvironment(sibDir, environment)
		if readErr != nil {
			continue
		}
		token := runtimeAuthToken(filepath.Join(sibDir, "runtime.auth"))
		if !usableRuntimeToken(token) || token == ownerToken {
			continue
		}
		envelope, envErr := runtimeTokenEnvelope(settings)
		if envErr != nil {
			continue
		}
		if other, ok := entries[token]; ok && other.PrincipalID != settings.User {
			continue
		}
		dropPrincipal(entries, settings.User)
		entries[token] = envelope
	}
	dropPrincipal(entries, owner.User)
	if ownerToken != "" {
		delete(entries, ownerToken)
	}
	if len(entries) == 0 {
		info, statErr := os.Lstat(path)
		if statErr == nil && info.Mode().IsRegular() {
			return os.Remove(path)
		}
		return nil
	}
	if sameTokenMap(path, entries) {
		return nil
	}
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return writeFileInPlace(path, body)
}

func runtimeTokenEnvelope(s Settings) (identity.Envelope, error) {
	contextID := s.User
	if s.OrgScoped() {
		contextID = s.Organization
	}
	envelope := identity.Envelope{
		Schema:             identity.Schema,
		PrincipalID:        s.User,
		ExternalIdentityID: s.User,
		ContextID:          contextID,
		RuntimeID:          s.User,
		ConversationID:     "toolhub",
		DeliveryTargetID:   "toolhub",
		PolicyVersion:      PolicyVersion(s),
	}
	if s.CapabilityMode == "managed" {
		if !idPattern.MatchString(s.CapabilityProfileID) || (s.Environment != "dev" && s.Environment != "prod") || s.CapabilityGeneration == 0 {
			return identity.Envelope{}, fmt.Errorf("managed runtime token identity is incomplete")
		}
		envelope.CapabilityProfile, envelope.Environment, envelope.Generation = s.CapabilityProfileID, s.Environment, s.CapabilityGeneration
	}
	if err := envelope.Validate(envelope.PrincipalID, envelope.ContextID, envelope.RuntimeID, envelope.PolicyVersion); err != nil {
		return identity.Envelope{}, err
	}
	return envelope, nil
}

func runtimeAuthToken(path string) string {
	values, err := ReadSecrets(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(values["HUB_RUNTIME_AUTH"])
}

func usableRuntimeToken(token string) bool {
	return len(token) >= 32 && !strings.ContainsAny(token, "\r\n\x00")
}

func dropPrincipal(entries map[string]identity.Envelope, principal string) {
	for token, envelope := range entries {
		if envelope.PrincipalID == principal {
			delete(entries, token)
		}
	}
}

func sameDir(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return strings.EqualFold(left, right)
}

func readTokenFile(path string) (map[string]identity.Envelope, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]identity.Envelope{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]identity.Envelope{}, nil
	}
	var entries map[string]identity.Envelope
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("toolhub tokens file: %w", err)
	}
	if entries == nil {
		return map[string]identity.Envelope{}, nil
	}
	cleaned := make(map[string]identity.Envelope, len(entries))
	for token, envelope := range entries {
		if !usableRuntimeToken(token) {
			continue
		}
		if envelope.Validate(envelope.PrincipalID, envelope.ContextID, envelope.RuntimeID, envelope.PolicyVersion) != nil {
			continue
		}
		cleaned[token] = envelope
	}
	return cleaned, nil
}

func sameTokenMap(path string, entries map[string]identity.Envelope) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var current map[string]identity.Envelope
	if json.Unmarshal(body, &current) != nil {
		return false
	}
	if current == nil {
		current = map[string]identity.Envelope{}
	}
	left, err := json.Marshal(current)
	if err != nil {
		return false
	}
	right, err := json.Marshal(entries)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

func writeFileInPlace(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(body)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(path, 0600)
}
