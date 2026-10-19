package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// SafeReceipt bounds a provider mutation receipt before it reaches the audit
// ledger. An oversized or control-character receipt is dropped rather than
// truncated, so a provider cannot smuggle content into control-plane records.
func SafeReceipt(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
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
