package toolhub

import (
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A literal Gateway has no wired load gate; Overloaded must still answer.
func TestGatewayOverloadedDefault(t *testing.T) {
	if (&Gateway{}).Overloaded() {
		t.Fatal("fresh gateway reports shed")
	}
}

// The denial carrier renders a stable operator-facing message.
func TestMemoryContextDeniedError(t *testing.T) {
	if (&memoryContextDenied{code: 403}).Error() == "" {
		t.Fatal("empty denial message")
	}
}

// Ambiguous-target errors enumerate the candidate ids for the caller.
func TestErrAmbiguousTargetError(t *testing.T) {
	err := (&ErrAmbiguousTarget{Candidates: []api.OperationCandidate{{ID: "a"}, {ID: "b"}}}).Error()
	if err == "" {
		t.Fatal("empty message")
	}
}

// Validate fails closed on operations outside the released vocabulary and
// passes a declared pair.
func TestRightsDocValidate(t *testing.T) {
	doc := rightsVocabulary()
	if err := doc.Validate("workspace", "read"); err != nil {
		t.Fatalf("declared right rejected: %v", err)
	}
	if err := doc.Validate("workspace", "nope"); err == nil {
		t.Fatal("undeclared operation accepted")
	}
	if err := doc.Validate("ghost", "read"); err == nil {
		t.Fatal("undeclared capability accepted")
	}
}

// Store getters and the bool gate behave on an empty store: miss, deny.
func TestStoreSmallHelpers(t *testing.T) {
	s := NewStore()
	if _, ok := s.CapabilityPolicy("missing"); ok {
		t.Fatal("phantom capability policy")
	}
	if _, ok := s.CapabilityProfile("missing"); ok {
		t.Fatal("phantom capability profile")
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ContextID: "alice"}
	if s.GrantsRight(auth, "tools", "use", "x", time.Now()) {
		t.Fatal("empty store grants right")
	}
}
