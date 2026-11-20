package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/shared/locale"
)

// runtimeIntrospectionGate is the hubctl PEP for operator introspection. It
// records the authorization decision before Docker is touched: the audit
// event carries scope and actor metadata, never argv, environment or output.
func runtimeIntrospectionGate(dir, environment, action string) (stack.Settings, error) {
	settings, readErr := stack.ReadEffective(dir, environment)
	principal := filepath.Base(filepath.Clean(dir))
	if settings.User != "" {
		principal = settings.User
	}

	allowed := readErr == nil && settings.RuntimeIntrospectionAllowed()
	event := audit.NewEvent("introspection", principal, "denied")
	event.RuntimeID = "hermes-runtime"
	event.Environment = environment
	event.PolicyRevision = settings.PassportRevision
	event.CapabilityProfile = settings.CapabilityProfileID
	event.OperationID = event.EventID
	event.CorrelationID = event.EventID
	event.Phase = "decision"
	event.Action = action
	event.Authorization = "deny"
	event.Execution = "not_started"
	event.Delivery = "not_applicable"
	event.Name = operatorActor()
	event.Reason = "policy"
	if readErr != nil {
		event.Reason = "config"
	}
	if allowed {
		event.Outcome = "admitted"
		event.Authorization = "allow"
		event.Reason = ""
	}

	ledger, err := audit.Open(filepath.Join(dir, "runtime", "toolhub", "audit.jsonl"))
	if err != nil {
		return settings, fmt.Errorf("runtime introspection audit: %w", err)
	}
	if err := ledger.Append(event); err != nil {
		return settings, fmt.Errorf("runtime introspection audit: %w", err)
	}
	if readErr != nil {
		return settings, readErr
	}
	if !allowed {
		return settings, fmt.Errorf("%s", locale.T(locale.Operator(), "hubctl.runtime-introspection-denied"))
	}
	return settings, nil
}

func operatorActor() string {
	name := strings.TrimSpace(os.Getenv("USERNAME"))
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.Username) != "" {
		name = current.Username
	}
	var clean strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._@-", r) {
			clean.WriteRune(r)
		} else {
			clean.WriteByte('_')
		}
	}
	runes := []rune(strings.Trim(clean.String(), "_"))
	if len(runes) == 0 {
		return "operator"
	}
	if len(runes) > 96 {
		runes = runes[:96]
	}
	return "operator:" + string(runes)
}
