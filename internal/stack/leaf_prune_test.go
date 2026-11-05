package stack

import (
	"strings"
	"testing"
)

// S12.1: memory_leaf_retention_days / memory_leaf_fold_bytes are opt-in
// knobs — 0 disables; retention below 2 or negative values are invalid;
// both require memory_vault like every memory_* surface.
func TestLeafRetentionValidation(t *testing.T) {
	base := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools("workspace", "memory_vault"),
		Ingress: testIngress("workspace")}

	ok := base
	ok.MemoryLeafRetentionDays = 7
	ok.MemoryLeafFoldBytes = 65536
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid knobs rejected: %v", err)
	}

	for _, d := range []int{-1, 1} {
		bad := base
		bad.MemoryLeafRetentionDays = d
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "memory_leaf_retention_days") {
			t.Fatalf("retention %d: want memory_leaf_retention_days error, got %v", d, err)
		}
	}

	neg := base
	neg.MemoryLeafFoldBytes = -1
	if err := neg.Validate(); err == nil || !strings.Contains(err.Error(), "memory_leaf_fold_bytes") {
		t.Fatalf("fold -1: want memory_leaf_fold_bytes error, got %v", err)
	}

	nofeat := base
	nofeat.Tools = testTools("workspace")
	nofeat.MemoryLeafRetentionDays = 7
	if err := nofeat.Validate(); err == nil || !strings.Contains(err.Error(), "memory_vault") {
		t.Fatalf("knobs without memory_vault: want feature error, got %v", err)
	}
}

// SPEC-0085 R7.5: the knobs render as gateway env only while memory_vault is
// enabled — zero/unset renders nothing (off is the default).
func TestLeafKnobsRenderEnv(t *testing.T) {
	dir := t.TempDir()
	envOf := func(s Settings) M {
		gateway, ok := Compose(s, "", dir)["services"].(M)["communication-hub"].(M)
		if !ok {
			t.Fatal("communication-hub service missing")
		}
		env, _ := gateway["environment"].(M)
		return env
	}
	s := validSettings()
	s.Ingress = []string{"telegram"}
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaNative}}
	if got := envOf(s)["HUB_MEMORY_LEAF_RETENTION_DAYS"]; got != nil {
		t.Fatalf("unset retention renders env: %v", got)
	}
	s.MemoryLeafRetentionDays = 7
	s.MemoryLeafFoldBytes = 65536
	env := envOf(s)
	if got := env["HUB_MEMORY_LEAF_RETENTION_DAYS"]; got != "7" {
		t.Fatalf("retention env = %v, want 7", got)
	}
	if got := env["HUB_MEMORY_LEAF_FOLD_BYTES"]; got != "65536" {
		t.Fatalf("fold env = %v, want 65536", got)
	}
	s.Tools = map[string]ToolEntry{"memory_vault": {Via: ToolViaOff}}
	if got := envOf(s)["HUB_MEMORY_LEAF_RETENTION_DAYS"]; got != nil {
		t.Fatalf("denied feature still renders retention: %v", got)
	}
}
