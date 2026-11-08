package catalog

import (
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// r5Families is the frozen first-party family dictionary of catalog release 5
// (ADR-0094 D1, SPEC-0093 R1.1). Every entry is a placeable toolset under its
// logical name — no hub: prefix.
var r5Families = []string{
	"files", "terminal", "documents", "media", "skills", "routines",
	"watches", "assignments", "goals", "context", "settings", "ssh",
	"exposure", "sites", "web_search", "browser", "research", "connectors",
	"models", "sessions", "memory",
}

// r5Members is the current model-tool membership per family.
var r5Members = map[string][]string{
	"files":       {"file_list", "file_read", "file_search", "file_write", "file_edit", "file_mkdir", "file_archive", "file_remove", "project_create"},
	"memory":      {"memory_search", "memory_read", "memory_card", "memory_open"},
	"documents":   {"document_extract", "document_create", "document_edit", "document_convert", "document_remove"},
	"media":       {"media_inspect", "media_convert", "media_generate", "media_edit", "media_fetch", "media_transcribe", "media_speak"},
	"terminal":    {"terminal_exec", "terminal_job"},
	"ssh":         {"ssh_list", "ssh_read", "ssh_write", "ssh_exec", "ssh_shell", "ssh_tunnel"},
	"exposure":    {"exposure_list", "exposure_create", "exposure_remove"},
	"routines":    {"routine_list", "routine_read", "routine_create", "routine_edit", "routine_remove"},
	"watches":     {"watch_list", "watch_create", "watch_edit", "watch_remove", "watch_sample"},
	"assignments": {"assignment_list", "assignment_read", "assignment_plan", "assignment_create", "assignment_edit", "assignment_remove"},
	"goals":       {"goal_status", "goal_edit", "goal_remove"},
	"context":     {"context_status", "context_edit"},
	"settings":    {"settings_read", "settings_edit"},
	"sessions":    {"session_list", "session_search", "session_read", "session_create", "session_switch", "session_edit", "session_remove"},
	"models":      {"model_list", "model_switch"},
	"skills":      {"skill", "skill_search", "skill_edit"},
	"connectors":  {"connector_search", "connector_list", "connector_status", "connector_install", "connector_edit"},
	"web_search":  {"web_search", "web_fetch", "web_providers", "web_search_multi", "web_cache"},
	"sites":       {"site_list", "site_publish", "site_remove"},
	"research":    {"research_create", "research_read", "research_list"},
	"browser":     {"browser_open", "browser_read", "browser_act", "browser_capture"},
	"platform":    {"platform_guide", "platform_status", "platform_bootstrap", "platform_usage", "tool_search", "tool_call"},
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	slices.Sort(out)
	return out
}

// r5Connectors are the connector toolsets that materialize on connector
// enable — curated dynamic surfaces plus the uncurated raw ones.
var r5Connectors = []string{"github", "gitlab", "google", "slack", "atlassian", "telegram_user", "user_mcp", "org_mcp"}

// TestR5Vocabulary locks the whole release-5 dictionary in one red test:
// toolset keys, members, executors, audiences and native coverage.
func TestR5Vocabulary(t *testing.T) {
	docs, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if Release != "22" {
		t.Fatalf("catalog release = %q, want 22", Release)
	}
	// --- families under logical names with the frozen member list
	for _, family := range r5Families {
		ts, err := docs.Toolset(family)
		if err != nil {
			t.Errorf("family %s missing: %v", family, err)
			continue
		}
		if ts.Kind != "family" {
			t.Errorf("family %s kind = %q, want family", family, ts.Kind)
		}
		want := sortedCopy(r5Members[family])
		if !slices.Equal(ts.Members, want) {
			t.Errorf("family %s members = %v, want %v", family, ts.Members, want)
		}
		if ts.Executor == "" {
			t.Errorf("family %s has no executor", family)
		}
	}
	ts, err := docs.Toolset("platform")
	if err != nil {
		t.Fatalf("platform toolset missing: %v", err)
	}
	if !ts.Pinned || !slices.Equal(ts.Members, sortedCopy(r5Members["platform"])) {
		t.Errorf("platform toolset = %+v", ts)
	}
	for name := range docs.Toolsets.Toolsets {
		if strings.HasPrefix(name, "hub:") {
			t.Errorf("hub-prefixed toolset %s survived into r5", name)
		}
	}
	for _, gone := range []string{"archives", "artifacts", "inbox", "knowledge", "hh", "services", "skills_manage", "hosting", "tools", "images", "image_gen"} {
		if _, err := docs.Toolset(gone); err == nil {
			t.Errorf("removed family %s still placeable", gone)
		}
	}
	// --- native vocabulary: every inventory name; carveouts approved,
	// pseudo marked, the rest denied by construction
	inventory, err := stack.ManagedCapabilityInventory()
	if err != nil {
		t.Fatal(err)
	}
	carveouts := map[string]bool{}
	for _, name := range stack.NativeCarveoutToolsets() {
		carveouts[name] = true
	}
	pseudo := map[string]bool{}
	for _, name := range stack.PseudoToolsetNames() {
		pseudo[name] = true
	}
	for _, name := range inventory.DisabledToolsets {
		nativeTS, err := docs.Toolset("native:" + name)
		if err != nil {
			t.Errorf("native toolset %s missing: %v", name, err)
			continue
		}
		if carveouts[name] && !nativeTS.Carveout {
			t.Errorf("native:%s should be a carveout", name)
		}
		if !carveouts[name] && !pseudo[name] && (nativeTS.Section != "" || len(nativeTS.Via) > 0) {
			t.Errorf("denied-by-design native:%s is placeable: %+v", name, nativeTS)
		}
	}
	if len(inventory.DisabledToolsets)-len(carveouts) < 35 {
		t.Errorf("denied-by-design set shrank to %d, want ~37", len(inventory.DisabledToolsets)-len(carveouts))
	}
	// --- connectors
	for _, name := range r5Connectors {
		cTS, err := docs.Toolset(name)
		if err != nil {
			t.Errorf("connector toolset %s missing: %v", name, err)
			continue
		}
		if cTS.Kind != "connector" {
			t.Errorf("connector %s kind = %q, want connector", name, cTS.Kind)
		}
	}
	for _, name := range []string{"user_mcp", "org_mcp"} {
		if cTS, err := docs.Toolset(name); err != nil || !cTS.Uncurated || !slices.Contains(cTS.Via, "mcp-raw") {
			t.Errorf("%s should be an uncurated mcp-raw connector: %+v err %v", name, cTS, err)
		}
	}
	// --- audiences: non-model tools are never members
	def := agenttools.HubToolsDefinition()
	audience := map[string]api.Audience{}
	for _, spec := range def.Tools {
		audience[spec.Name] = spec.Audience
		if spec.Audience == "" {
			t.Errorf("tool %s has no audience", spec.Name)
		}
		if spec.Executor == "" {
			t.Errorf("tool %s has no executor", spec.Name)
		}
	}
	if audience["inbox_fetch"] != api.AudienceInternal {
		t.Errorf("inbox_fetch audience = %q, want internal", audience["inbox_fetch"])
	}
	memberOf := map[string][]string{}
	for name, ts := range docs.Toolsets.Toolsets {
		for _, m := range ts.Members {
			memberOf[m] = append(memberOf[m], name)
			if audience[m] != api.AudienceModel {
				t.Errorf("toolset %s member %s has audience %q", name, m, audience[m])
			}
		}
	}
	for _, spec := range def.Tools {
		if spec.Audience != api.AudienceModel {
			continue
		}
		if len(memberOf[spec.Name]) != 1 {
			t.Errorf("model tool %s lands in %d toolsets, want 1", spec.Name, len(memberOf[spec.Name]))
		}
	}
	total := 0
	for _, members := range r5Members {
		total += len(members)
	}
	if total != 97 {
		t.Fatalf("r5 member table holds %d tools, want 97", total)
	}
	// --- via/executor consistency: the route must be able to serve the
	// declared executor.
	allowed := map[string][]string{
		"native":  {"native"},
		"mcp-raw": {"native"},
		"mcp":     {"connector:"},
		"toolhub": {"handler:", "connector:", "flow:", "runtime:"},
	}
	for name, ts := range docs.Toolsets.Toolsets {
		if ts.Executor == "" {
			continue
		}
		for _, via := range ts.Via {
			prefixes, ok := allowed[via]
			if !ok {
				t.Errorf("toolset %s via %q unknown", name, via)
				continue
			}
			match := false
			for _, p := range prefixes {
				if strings.HasPrefix(ts.Executor, p) || ts.Executor == strings.TrimSuffix(p, ":") {
					match = true
				}
			}
			if !match {
				t.Errorf("toolset %s executor %q not servable by via %q", name, ts.Executor, via)
			}
		}
	}
}
