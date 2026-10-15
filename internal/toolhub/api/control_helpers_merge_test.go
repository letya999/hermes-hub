package api

import (
	"errors"
	"testing"
)

func TestParseGitHubSourceContract(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	src, err := ParseGitHubSource("https://github.com/acme/tool/commit/" + sha)
	if err != nil {
		t.Fatal(err)
	}
	if src.Repository != "https://github.com/acme/tool" || src.CommitSHA != sha {
		t.Fatalf("commit source: %+v", src)
	}
	tree, err := ParseGitHubSource("https://github.com/acme/tool/tree/" + sha + "/sub/dir")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Subfolder != "sub/dir" {
		t.Fatalf("tree subfolder: %+v", tree)
	}
	for _, bad := range []string{
		"https://github.com/acme/tool",                      // no commit/tree ref
		"https://github.com/acme/tool/commit/" + sha + "/x", // commit with subfolder
		"http://github.com/acme/tool/commit/" + sha,         // http
		"https://github.com.evil.io/acme/tool/commit/" + sha,
		"https://user@github.com/acme/tool/commit/" + sha,
		"https://github.com/acme/tool/tree/short",
		"https://github.com/acme/tool/commit/" + sha + "?query=1",
	} {
		if _, err := ParseGitHubSource(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", bad, err)
		}
	}
}

func TestRejectEscalationContract(t *testing.T) {
	def := ToolDefinition{
		Execution: ExecutionPolicy{CPUMillis: 500, MemoryMiB: 128, TimeoutSeconds: 30, MaxPIDs: 8, OutputBytes: 4096},
		Tools:     []ToolSpec{{Name: "read_tool", Effect: ReadEffect}},
	}
	// within-budget, within-effect args pass
	if err := RejectEscalation(def, map[string]any{
		"effects": []any{"read"}, "tools": []string{"read_tool"},
		"cpu_millis": 250.0, "memory_mib": int64(64), "timeout_seconds": 10,
	}); err != nil {
		t.Fatal(err)
	}
	// escalation cases
	for name, args := range map[string]map[string]any{
		"effect":  {"effects": []any{"write"}},
		"tool":    {"tools": []any{"undeclared"}},
		"cpu":     {"cpu_millis": 501},
		"memory":  {"memory_mib": 129},
		"timeout": {"timeout_seconds": 31},
		"pids":    {"max_pids": 9},
		"output":  {"output_bytes": 4097},
	} {
		if err := RejectEscalation(def, args); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s escalation accepted: %v", name, err)
		}
	}
	if err := RejectEscalation(def, nil); err != nil {
		t.Fatalf("nil args: %v", err)
	}
}
