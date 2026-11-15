package api

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/audit"
	"regexp"
	"strings"
)

var cliReceiptPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+@filesystem,network,pid@cell=[A-Za-z0-9_.-]+,rt=(runc|runsc|kata)$`)

// SafeReceipt accepts canonical provider resource identifiers, not serialized
// payload or auth links. Controls are rejected before outer whitespace is normalized.
func SafeReceipt(value string) string {
	if value == "" || !audit.ValidReceipt(value) {
		return ""
	}
	value = strings.TrimSpace(value)
	if cliReceiptPattern.MatchString(value) {
		return value
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:@+", c)) {
			return ""
		}
	}
	return value
}

// RejectAuthorityArguments denies model-supplied identity, binding or
// credential selectors. Authorization comes only from the authenticated
// runtime and the resolved binding.
func RejectAuthorityArguments(arguments map[string]any) error {
	for key := range arguments {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "principal_id", "context_id", "runtime_id", "policy_version", "binding_id", "tool_binding_id", "connection_id", "credential_ref", "credential_ref_id", "owner", "owner_id", "user", "user_id", "account_id", "locator", "credential_locator", "backend", "backend_url", "mcp_endpoint", "policy", "grant", "grant_id", "issued_by", "store_owner":
			return fmt.Errorf("%w: authority argument %q", ErrUnauthorized, key)
		}
		if CredentialPattern.MatchString(key) {
			return fmt.Errorf("%w: credential value argument %q", ErrUnauthorized, key)
		}
	}
	return nil
}

// Provider resource owners are data only when explicitly declared by the
// admitted tool. Runtime identity and credential selectors remain reserved.
func RejectToolAuthorityArguments(tool ToolSpec, arguments map[string]any) error {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(tool.InputSchema, &schema)
	for key, value := range arguments {
		switch key {
		case "owner", "owner_id", "user", "user_id", "account_id":
			if _, declared := schema.Properties[key]; declared {
				continue
			}
		}
		if err := RejectAuthorityArguments(map[string]any{key: value}); err != nil {
			return err
		}
	}
	return nil
}
