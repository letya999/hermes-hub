package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectionAndMiscHelpers(t *testing.T) {
	def := ToolDefinition{Tools: []ToolSpec{{Name: "a", Effect: ReadEffect}, {Name: "b", Effect: WriteEffect}, {Name: "c", Effect: ReadEffect}}}
	if names := ToolNames(def); len(names) != 3 || names[0] != "a" {
		t.Fatalf("tool names: %v", names)
	}
	if effects := EffectNames(def); len(effects) != 2 {
		t.Fatalf("effect names: %v", effects)
	}
	if ArgString(map[string]any{"k": " v "}, "k") != "v" || ArgString(nil, "k") != "" {
		t.Fatal("ArgString")
	}
	if got := ArgStrings(map[string]any{"k": []any{" a ", "", 3, "b"}}, "k"); len(got) != 2 || got[1] != "b" {
		t.Fatalf("ArgStrings: %v", got)
	}
	if ArgInt(map[string]any{"k": 4.9}, "k") != 4 || ArgInt(nil, "k") != 0 {
		t.Fatal("ArgInt")
	}
	if n := RandomNonce(); len(n) != 32 {
		t.Fatalf("nonce: %q", n)
	}

	// correlation IDs are sanitized and carried on the context
	ctx := WithCallCorrelation(t.Context(), CallCorrelation{JobID: "j1"})
	if CallCorrelationFrom(ctx).JobID != "j1" {
		t.Fatal("correlation lost")
	}
	corr := HeaderCorrelation(" j ", "r\nbad")
	if corr.JobID != "j" || corr.HermesRunID != "" {
		t.Fatalf("header correlation: %+v", corr)
	}
	if id := NewToolCallID(); !strings.HasPrefix(id, "call-") || len(id) != 37 {
		t.Fatalf("call id: %q", id)
	}

	// binding workspace removal is confined to the root
	root := t.TempDir()
	eff := EffectiveBinding{Definition: ToolDefinition{DefinitionID: "def-1", Workload: WorkloadPolicy{Class: PerUser}},
		Binding: ToolBinding{PrincipalID: "p1", ContextID: "c1", ToolBindingID: "bind-1"}}
	ws, err := OpenWorkloadWorkspace(root, eff, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Path, "f"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveBindingWorkspace(root, eff); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatal("workspace not removed")
	}
	if err := RemoveBindingWorkspace("", eff); err != nil {
		t.Fatal("empty root must be a no-op")
	}

	// reconnect marker + exclusion vocabulary
	dir := t.TempDir()
	if err := WriteReconnectMarker(dir, ProjectionChange{Revision: 3, Reason: "stale"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ReconnectMarkerName)); err != nil {
		t.Fatal("marker missing")
	}
	if err := WriteReconnectMarker(dir, ProjectionChange{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty change: %v", err)
	}
	if err := WriteReconnectMarker(filepath.Join(dir, "nope"), ProjectionChange{Revision: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing dir: %v", err)
	}
	if ClassifyProjectionExclusion(ToolBinding{Status: RevokedStatus}, nil) != ExcludeRevoked ||
		ClassifyProjectionExclusion(ToolBinding{Status: DisabledStatus}, nil) != ExcludeDisabled ||
		ClassifyProjectionExclusion(ToolBinding{}, ErrDegraded) != ExcludeDegraded ||
		ClassifyProjectionExclusion(ToolBinding{}, ErrStale) != ExcludeStale ||
		ClassifyProjectionExclusion(ToolBinding{}, errors.New("x")) != ExcludePolicyMismatch {
		t.Fatal("exclusion classification")
	}
	if err := ValidateProjectedName("ok-name"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProjectedName("bad name!"); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad name accepted")
	}
	attrs := ToolSection{Owner: "external", Official: true, Effect: "read"}.SectionAttrs()
	if attrs["owner"] != "external" || attrs["official"] != "true" || attrs["dynamic"] != "false" {
		t.Fatalf("section attrs: %v", attrs)
	}
}
