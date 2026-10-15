package api

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testDigest = "sha256:" + strings.Repeat("a", 64)

func testCapRule(prefix string, out, timeout int) CapabilityRule {
	return CapabilityRule{
		CapabilityID: "fs.read", ImplementationDigest: testDigest,
		Action: "read", Resource: "files", PathPrefix: prefix,
		Limits: CapabilityLimits{OutputBytes: out, TimeoutSeconds: timeout},
	}
}

func testCapPolicy() CapabilityPolicy {
	return CapabilityPolicy{
		Schema: SchemaVersion, PolicyID: "pol-1", Members: []string{"alice"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(),
		Reason: "baseline", Status: ActiveStatus,
		Ceiling:  []CapabilityRule{testCapRule("", 4096, 60)},
		Defaults: []CapabilityRule{testCapRule("docs", 1024, 30)},
	}
}

func TestCapabilityPolicyValidate(t *testing.T) {
	p := testCapPolicy()
	if err := p.Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfirmed policy: %v", err)
	}
	if err := Confirm(&p, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("confirmed policy: %v", err)
	}
	wide := testCapPolicy()
	wide.Defaults = []CapabilityRule{testCapRule("docs", MaxOutputBytes, 60)}
	if err := Confirm(&wide, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := wide.Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("default above ceiling: %v", err)
	}
	denyLimits := testCapPolicy()
	deny := testCapRule("", 0, 0)
	deny.Limits = CapabilityLimits{OutputBytes: 1}
	denyLimits.Denies = []CapabilityRule{deny}
	if err := denyLimits.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deny with limits: %v", err)
	}
	badMember := testCapPolicy()
	badMember.Members = []string{"alice", "alice"}
	if err := badMember.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate member: %v", err)
	}
	solo := testCapPolicy()
	solo.Members = []string{"alice", "bob"}
	if err := solo.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("personal policy with two members: %v", err)
	}
}

func testCapProfile() CapabilityProfile {
	return CapabilityProfile{
		Schema: SchemaVersion, ProfileID: "prof-1", PrincipalID: "alice",
		ContextID: "ctx-1", RuntimeID: "rt-1", Environment: "dev",
		Generation: 1, PolicyVersion: "v1", PolicyID: "pol-1", PolicyRevision: 1,
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(),
		Reason: "baseline", Status: ActiveStatus,
		Selections: []CapabilitySelection{{
			CapabilityID: "fs.read", DefinitionID: "def-1", DefinitionVersion: "1.0.0",
			ImplementationDigest: testDigest, ToolName: "fs_read", Name: "fs.read",
		}},
	}
}

func TestCapabilityRuleDecisionBranches(t *testing.T) {
	now := time.Now()
	policy := testCapPolicy()
	profile := testCapProfile()
	tuple := CapabilityRule{CapabilityID: "fs.read", ImplementationDigest: testDigest, Action: "read", Resource: "files"}
	path := "docs/a.txt"
	limits, prefix, ok := CapabilityRuleDecision(profile, policy, tuple, &path, now)
	if !ok || limits.OutputBytes != 1024 || limits.TimeoutSeconds != 30 || prefix != "docs" {
		t.Fatalf("allow: %v %+v %q", ok, limits, prefix)
	}
	outside := "etc/passwd"
	if _, _, ok := CapabilityRuleDecision(profile, policy, tuple, &outside, now); ok {
		t.Fatal("path outside granted prefix allowed")
	}
	policy.Denies = []CapabilityRule{{
		CapabilityID: "fs.read", ImplementationDigest: testDigest,
		Action: "read", Resource: "files", PathPrefix: "docs",
	}}
	blocked := "docs/a.txt"
	if _, _, ok := CapabilityRuleDecision(profile, policy, tuple, &blocked, now); ok {
		t.Fatal("deny prefix did not block")
	}
	policy.Denies = nil
	expired := CapabilityRule{CapabilityID: "fs.read", ImplementationDigest: testDigest,
		Action: "read", Resource: "files", ExpiresAt: now.Add(-time.Hour),
		Limits: CapabilityLimits{OutputBytes: 64, TimeoutSeconds: 5}}
	policy.Defaults = []CapabilityRule{expired}
	if _, _, ok := CapabilityRuleDecision(profile, policy, tuple, &path, now); ok {
		t.Fatal("expired default allowed")
	}
}

func TestCapabilityProfileAndDecision(t *testing.T) {
	profile := testCapProfile()
	if err := profile.Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfirmed profile: %v", err)
	}
	if err := profile.ValidateStructure(); err != nil {
		t.Fatalf("structure: %v", err)
	}
	if err := Confirm(&profile, "operator", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("confirmed profile: %v", err)
	}
	badEnv := testCapProfile()
	badEnv.Environment = "stage"
	if err := badEnv.ValidateStructure(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad environment: %v", err)
	}
	badOp := testCapProfile()
	badOp.ControlOperations = []string{"not-an-op"}
	if err := badOp.ValidateStructure(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unreviewed control operation: %v", err)
	}
	dupSel := testCapProfile()
	dupSel.Selections = append(dupSel.Selections, dupSel.Selections[0])
	if err := dupSel.ValidateStructure(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate selection: %v", err)
	}
	policy := testCapPolicy()
	selection := profile.Selections[0]
	tool := ToolSpec{Name: "fs_read", Uses: []CapabilityUse{
		{Action: "read", Resource: "files", PathArgument: "path"},
	}, ArgumentEquals: map[string]string{"mode": "ro"}}
	limits, scopes, ok := CapabilityDecision(profile, policy, selection, tool, map[string]any{"mode": "ro", "path": "docs/a.txt"}, false)
	if !ok || len(scopes) != 1 || scopes[0].PathPrefix != "docs" || limits.TimeoutSeconds != 30 {
		t.Fatalf("decision: %v %+v %+v", ok, limits, scopes)
	}
	if _, _, ok := CapabilityDecision(profile, policy, selection, tool, map[string]any{"mode": "rw", "path": "docs/a.txt"}, false); ok {
		t.Fatal("argument_equals bypassed")
	}
	if _, _, ok := CapabilityDecision(profile, policy, selection, tool, map[string]any{"mode": "ro", "path": "../escape"}, false); ok {
		t.Fatal("traversal path allowed")
	}
	if _, _, ok := CapabilityDecision(profile, policy, selection, tool, map[string]any{"mode": "ro", "path": 42}, false); ok {
		t.Fatal("non-string path allowed")
	}
	if _, _, ok := CapabilityDecision(profile, policy, selection, tool, map[string]any{"mode": "ro"}, true); !ok {
		t.Fatal("projection rejected without concrete args")
	}
}

func TestCapabilityGroupsPathsAndPreview(t *testing.T) {
	member := testCapRule("docs", 1024, 30)
	group := CapabilityGroup{GroupID: "g1", Revision: 1, Members: []CapabilityRule{member}}
	if err := ValidateCapabilityGroup(group); err != nil {
		t.Fatalf("valid group: %v", err)
	}
	dup := CapabilityGroup{GroupID: "g1", Revision: 1, Members: []CapabilityRule{member, member}}
	if err := ValidateCapabilityGroup(dup); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate member group: %v", err)
	}
	empty := CapabilityGroup{GroupID: "g1", Revision: 1}
	if err := ValidateCapabilityGroup(empty); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty group: %v", err)
	}
	next := CapabilityGroup{GroupID: "g1", Revision: 2, Members: []CapabilityRule{member}}
	if err := CheckGroupRevisions([]CapabilityGroup{group}, []CapabilityGroup{next}); err != nil {
		t.Fatalf("advancing revision: %v", err)
	}
	regress := CapabilityGroup{GroupID: "g1", Revision: 1, Members: []CapabilityRule{testCapRule("other", 64, 5)}}
	if err := CheckGroupRevisions([]CapabilityGroup{group}, []CapabilityGroup{regress}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-revision member change: %v", err)
	}
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"", true}, {"docs/a.txt", true}, {"docs", true},
		{"/abs", false}, {"../up", false}, {"a\\b", false}, {"a:b", false},
		{".", false}, {"a//b", false}, {"a/./b", false},
	} {
		if got := ValidCapabilityPath(tc.v); got != tc.want {
			t.Fatalf("ValidCapabilityPath(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
	outer := testCapRule("docs", 1024, 30)
	inner := testCapRule("docs/a", 512, 15)
	if !RuleInside(outer, inner) {
		t.Fatal("inner rule not inside outer")
	}
	if RuleInside(inner, outer) {
		t.Fatal("outer inside inner")
	}
	a := ProfilePreviewEntry{OutputBytes: 10, TimeoutSeconds: 5, Scopes: []CapabilityScope{{Resource: "f", PathPrefix: "p"}}}
	b := a
	if !PreviewEntriesEqual(a, b) {
		t.Fatal("equal preview entries")
	}
	b.OutputBytes = 11
	if PreviewEntriesEqual(a, b) {
		t.Fatal("changed preview entry equal")
	}
	if err := validateToolCapability(ToolSpec{Name: "t"}); err != nil {
		t.Fatalf("tool without capability: %v", err)
	}
	if err := validateToolCapability(ToolSpec{Name: "t", CapabilityID: "fs.read"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("capability without uses: %v", err)
	}
	if err := validateToolCapability(ToolSpec{Name: "t", CapabilityID: "fs.read",
		Uses:           []CapabilityUse{{Action: "read", Resource: "files"}},
		ArgumentEquals: map[string]string{"bad key!": "v"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad argument key: %v", err)
	}
}
