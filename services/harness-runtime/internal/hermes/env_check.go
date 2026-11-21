package hermes

import (
	"errors"
	"os"
	"strings"

	"github.com/letya999/hermes-hub/shared/locale"
)

// Issue #602 S4: the managed runtime's environment is exactly the rendered
// spec — every HUB_* name below is one the renderer may emit or the service
// may read. An unknown HUB_* variable fails startup: a typo or an injected
// key must never silently change runtime behavior.
var hubEnvExact = map[string]bool{
	"HUB_AUDIT_LEDGER":              true,
	"HUB_BOOTSTRAP_GOAL":            true,
	"HUB_BOOTSTRAP_REVISION":        true,
	"HUB_BROWSER":                   true,
	"HUB_BROWSER_HOST":              true,
	"HUB_CAPABILITY_ENVIRONMENT":    true,
	"HUB_CAPABILITY_GENERATION":     true,
	"HUB_CAPABILITY_PROFILE_ID":     true,
	"HUB_COMMUNICATION_AUTH":        true,
	"HUB_COMMUNICATION_CONTROL_URL": true,
	"HUB_CONTEXT_ID":                true,
	"HUB_CONVERSATION_ID":           true,
	"HUB_DEFAULT_LANG":              true,
	"HUB_DELIVERY_TARGET_ID":        true,
	"HUB_EGRESS_PROXY":              true,
	"HUB_ENVIRONMENT":               true,
	"HUB_EXECUTION_SUBSTRATE":       true,
	"HUB_EXPOSURE_CONFIG":           true,
	"HUB_EXTERNAL_ID":               true,
	"HUB_FC_INGRESS":                true,
	"HUB_FC_PROXIES":                true,
	"HUB_FEATURES":                  true,
	"HUB_HERMES_API_HOST":           true,
	"HUB_HERMES_API_PORT":           true,
	"HUB_HERMES_CONFIG":             true,
	"HUB_HH_ENABLED":                true,
	"HUB_HOSTING_TOKEN":             true,
	"HUB_HOSTING_TOKEN_FILE":        true,
	"HUB_HOSTING_URL":               true,
	"HUB_KUBERNETES_POD_NAME":       true,
	"HUB_KUBERNETES_POD_NAMESPACE":  true,
	"HUB_LISTEN":                    true,
	"HUB_MANAGED_BROWSER":           true,
	"HUB_MANAGED_CONTEXT":           true,
	"HUB_MANAGED_IMAGE_GEN":         true,
	"HUB_MANAGED_KNOWLEDGE_UI":      true,
	"HUB_MANAGED_MEMORY_VAULT":      true,
	"HUB_MANAGED_MODEL_ID":          true,
	"HUB_MANAGED_MODEL_URL":         true,
	"HUB_MANAGED_WEB":               true,
	"HUB_MEET":                      true,
	"HUB_MODEL_RELAY_ADMIN":         true,
	"HUB_MODEL_RELAY_AUTH":          true,
	"HUB_MODEL_RELAY_URL":           true,
	"HUB_NATIVE_TOOLSETS":           true,
	"HUB_ORGANIZATION_ID":           true,
	"HUB_ORGANIZATION_ROLE":         true,
	"HUB_ORG_ACTIONS":               true,
	"HUB_ORG_SCOPED":                true,
	"HUB_PASSPORT_REVISION":         true,
	"HUB_PERSISTENT_HERMES":         true,
	"HUB_POLICY_VERSION":            true,
	"HUB_PRINCIPAL_ID":              true,
	"HUB_PROTECTED_ENV_KEYS":        true,
	"HUB_PUBLIC_TARGET":             true,
	"HUB_RUNTIME_AUTH":              true,
	"HUB_RUNTIME_GENERATION":        true,
	"HUB_RUNTIME_ID":                true,
	"HUB_RUNTIME_LISTEN":            true,
	"HUB_RUNTIME_MODE":              true,
	"HUB_RUNTIME_SUPERVISOR_URL":    true,
	"HUB_RUN_BUDGET":                true,
	"HUB_RUN_STALL_TIMEOUT":         true,
	"HUB_RUN_VERIFY_AUTH":           true,
	"HUB_RUN_VERIFY_URL":            true,
	"HUB_SCOPE_KIND":                true,
	"HUB_SELFTEST_CHILD":            true,
	"HUB_SELF_ENV_KEYS":             true,
	"HUB_SERVICE_TOKEN":             true,
	"HUB_SESSION_REVISION":          true,
	"HUB_SESSION_SLOTS":             true,
	"HUB_SHARED_GID":                true,
	"HUB_SKILLS_EFFECTIVE":          true,
	"HUB_SKILLS_EFFECTIVE_RW":       true,
	"HUB_SKILLS_META":               true,
	"HUB_SKILLS_ORG":                true,
	"HUB_SKILLS_PLATFORM":           true,
	"HUB_SKILLS_STORE":              true,
	"HUB_SSH_CONFIG":                true,
	"HUB_SSH_SHELL":                 true,
	"HUB_SSH_TUNNEL":                true,
	"HUB_SSH_WRITE":                 true,
	"HUB_STATE":                     true,
	"HUB_STT_URL":                   true,
	"HUB_TOOLHUB_CONTROL":           true,
	"HUB_TOOLHUB_ENDPOINT":          true,
	"HUB_TOOLHUB_RECONNECT":         true,
	"HUB_TOOLHUB_STORE":             true,
	"HUB_TOOLHUB_TOKEN_ENV":         true,
	"HUB_TOOLHUB_URL":               true,
	"HUB_TOOLS_RO":                  true,
	"HUB_TOOL_MANIFEST":             true,
	"HUB_TOOL_POLICY":               true,
	"HUB_TRUSTED":                   true,
	"HUB_USER_ID":                   true,
	"HUB_VAULT":                     true,
	"HUB_WORKSPACE":                 true,
	"HUB_WORKSPACE_BOUNDARY":        true,
	"HUB_WORKSPACE_RO":              true,
}

// hubEnvPrefixes are whole families the render materializes under a prefix:
// deferred child env, per-runtime credential-broker keys, and media grants.
var hubEnvPrefixes = []string{"HUB_DEFER_", "HUB_CREDENTIAL_BROKER_RUNTIME_", "HUB_MEDIA_"}

func allowedHubEnv(name string) bool {
	if hubEnvExact[name] {
		return true
	}
	for _, prefix := range hubEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// checkRuntimeEnv refuses startup when the managed environment carries a
// HUB_* name outside the rendered set. Runs inside managedPreflight so the
// trusted dev surface (which legitimately inherits the operator's shell env)
// is unaffected.
func checkRuntimeEnv() error {
	return checkRuntimeEnvList(os.Environ())
}

func checkRuntimeEnvList(environ []string) error {
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "HUB_") && !allowedHubEnv(name) {
			return errors.New(locale.T(locale.Fallback(), "runtime.env-unknown", name))
		}
	}
	return nil
}
