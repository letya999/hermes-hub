package hermes

import (
	"os"
	"strings"
	"testing"
)

// Issue #602 S4: the managed environment is exactly the rendered spec — an
// unknown HUB_* name fails preflight, a known name passes.
func TestRuntimeEnvAllowlistRejectsUnknown(t *testing.T) {
	if err := checkRuntimeEnvList([]string{"HUB_STATE=/state", "HUB_INJECTED_MADE_UP=1", "PATH=/bin"}); err == nil {
		t.Fatal("unknown HUB_* variable accepted")
	} else if !strings.Contains(err.Error(), "HUB_INJECTED_MADE_UP") {
		t.Fatalf("error does not name the offender: %v", err)
	}
}

func TestRuntimeEnvAllowlistAdmitsRenderedSet(t *testing.T) {
	for name := range hubEnvExact {
		if !allowedHubEnv(name) {
			t.Fatalf("allowlist rejects its own entry %s", name)
		}
	}
	for _, name := range []string{"HUB_DEFER_X", "HUB_CREDENTIAL_BROKER_RUNTIME_GITLAB", "HUB_MEDIA_UPSTREAM_KEY"} {
		if !allowedHubEnv(name) {
			t.Fatalf("prefix family rejected: %s", name)
		}
	}
	for _, name := range []string{"HUB_", "HUB_CREDENTIAL_BROKER_", "HUB_DEFERRAL", "HUB_MEDIATYPE"} {
		if allowedHubEnv(name) {
			t.Fatalf("near-prefix admitted: %s", name)
		}
	}
}

// Every HUB_* name the renderer writes into the runtime env must be in the
// allowlist — drift here means the runtime refuses a legitimate render.
func TestRuntimeEnvAllowlistCoversRenderedEnv(t *testing.T) {
	for _, name := range []string{
		"HUB_RUNTIME_AUTH", "HUB_PASSPORT_REVISION", "HUB_DEFAULT_LANG",
		"HUB_SSH_CONFIG", "HUB_EXPOSURE_CONFIG", "HUB_NATIVE_TOOLSETS",
		"HUB_MANAGED_WEB", "HUB_MANAGED_IMAGE_GEN", "HUB_MANAGED_BROWSER",
		"HUB_MANAGED_CONTEXT", "HUB_MANAGED_MEMORY_VAULT", "HUB_MANAGED_MODEL_ID",
		"HUB_MANAGED_MODEL_URL", "HUB_STT_URL", "HUB_MODEL_RELAY_URL",
		"HUB_TOOLHUB_ENDPOINT", "HUB_COMMUNICATION_CONTROL_URL",
		"HUB_COMMUNICATION_AUTH", "HUB_EGRESS_PROXY", "HUB_SESSION_SLOTS",
	} {
		if !allowedHubEnv(name) {
			t.Fatalf("rendered env var missing from allowlist: %s", name)
		}
	}
	// Test hooks the suite itself sets must not break the check for other
	// vars: they simply are not asserted.
	_ = os.Getenv("HUB_TEST_HELPER")
}
