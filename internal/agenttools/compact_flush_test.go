package agenttools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/compaction"
	"github.com/letya999/hermes-hub/internal/vault"
)

// SPEC-0085 R2.6: the in-loop hygiene compact binds the vault flush only
// when HUB_VAULT is set — the hook is otherwise nil and compaction unchanged.
func TestContextCompactFlushRequiresVault(t *testing.T) {
	t.Setenv("HUB_VAULT", "")
	if contextCompactFlush("s") != nil {
		t.Fatal("flush must be nil without a vault mount")
	}
}

// SPEC-0085 R2.6: an in-loop hygiene compaction flushes the dropped head to
// the vault leaf before apply — the wiring runs through the tool itself.
func TestContextCompactFlushesToVault(t *testing.T) {
	dir := t.TempDir()
	vroot := filepath.Join(dir, "vault")
	if err := vault.Ensure(vroot); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_VAULT", vroot)
	t.Setenv("HUB_CONTEXT_ID", "alice")
	tools := &Tools{StateDir: dir}
	old := compaction.Runner
	bigUser := strings.Repeat("u", 800000)
	messages := []map[string]any{}
	for i := 0; i < 14; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": bigUser})
	}
	body, _ := json.Marshal(messages)
	compaction.Runner = func(_ context.Context, req []byte) ([]byte, error) {
		var m map[string]any
		_ = json.Unmarshal(req, &m)
		switch m["op"] {
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":300000,"message_count":4}`), nil
		case "lease", "lock":
			return []byte(`{"ok":true,"acquired":true,"locked":true,"holder":"hub-compact"}`), nil
		case "read_transcript":
			return []byte(`{"ok":true,"session_id":"hub-x","watermark":4,"messages":` + string(body) + `}`), nil
		case "apply_transcript":
			return []byte(`{"ok":true,"applied":true,"active_count":2}`), nil
		case "unlease", "unlock":
			return []byte(`{"ok":true,"released":true}`), nil
		}
		return []byte(`{"ok":false,"error":"internal"}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	oldSum := contextCompactSummarize
	contextCompactSummarize = func(_ context.Context, _ string, _ []map[string]any, _ string) (string, error) {
		return "s", nil
	}
	t.Cleanup(func() { contextCompactSummarize = oldSum })

	if _, err := tools.ContextCompact(context.Background(), Input{ID: "hub-x", Focus: "x"}); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(vroot, "memory", "daily", "*.md"))
	if len(matches) == 0 {
		t.Fatal("no daily leaf written by the flush")
	}
	raw, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(raw), "kind=compaction") {
		t.Fatalf("flushed entries not stamped compaction:\n%s", raw)
	}
}
