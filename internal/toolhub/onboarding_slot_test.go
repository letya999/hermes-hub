package toolhub

import (
	"sync"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The onboarding slot is keyed by (principal, context, runtime, policy,
// definition): a repeat install under any request key attaches to the record
// holding the slot instead of minting a sibling.
func TestOnboardingSlotAttachesRepeatInstall(t *testing.T) {
	s, auth := NewStore(), aliceAuth()
	d := catalogReadDefinition()
	c := &ControlPlane{Store: s}
	first, created, err := c.newOnboarding(auth, api.OnboardingCatalog, "install-a", d, "", "", false)
	if err != nil || created {
		t.Fatalf("first install: attached=%v err=%v", created, err)
	}
	second, attached, err := c.newOnboarding(auth, api.OnboardingCatalog, "install-b", d, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !attached || second.OnboardingID != first.OnboardingID {
		t.Fatalf("repeat install minted a sibling: %s vs %s", second.OnboardingID, first.OnboardingID)
	}
	s.mu.RLock()
	live := 0
	for _, o := range s.onboardings {
		if o.Stage != api.StageGone {
			live++
		}
	}
	s.mu.RUnlock()
	if live != 1 {
		t.Fatalf("slot holds %d live records, want 1", live)
	}
}

// A failed holder keeps the slot: the retry minted under a different seed
// retires it and becomes the one pending record — the corpse names its
// replacement instead of lingering as a sibling.
func TestOnboardingSlotSupersedesFailedHolder(t *testing.T) {
	s, auth := NewStore(), aliceAuth()
	d := catalogReadDefinition()
	c := &ControlPlane{Store: s}
	failed, _, err := c.newOnboarding(auth, api.OnboardingCatalog, "attempt-a", d, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	failed.Stage, failed.Revision = api.StageFailed, failed.Revision+1
	if err := s.PutOnboarding(failed); err != nil {
		t.Fatal(err)
	}
	retry, attached, err := c.newOnboarding(auth, api.OnboardingCatalog, "attempt-b", d, "", "", false)
	if err != nil || attached {
		t.Fatalf("retry on failed slot: attached=%v err=%v", attached, err)
	}
	corpse, err := s.OnboardingFor(auth, failed.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if !corpse.Removed() || corpse.SupersededBy != retry.OnboardingID {
		t.Fatalf("failed holder not superseded: stage=%s by=%q", corpse.Stage, corpse.SupersededBy)
	}
	if retry.OnboardingID == failed.OnboardingID {
		t.Fatal("retry reused the failed record's id")
	}
}

// Two concurrent installs race through the same store lock: one mints the
// record, the other attaches to it — the slot can never hold siblings.
func TestOnboardingSlotConcurrentInstalls(t *testing.T) {
	s, auth := NewStore(), aliceAuth()
	d := catalogReadDefinition()
	c := &ControlPlane{Store: s}
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			o, _, err := c.newOnboarding(auth, api.OnboardingCatalog, "", d, "", "", false)
			if err == nil {
				ids[n] = o.OnboardingID
			}
		}(i)
	}
	wg.Wait()
	s.mu.RLock()
	live := 0
	for _, o := range s.onboardings {
		if o.Stage != api.StageGone {
			live++
		}
	}
	s.mu.RUnlock()
	if live != 1 {
		t.Fatalf("concurrent installs left %d live records, want 1", live)
	}
	for _, id := range ids {
		if id == "" {
			t.Fatal("a concurrent install was denied")
		}
	}
	if ids[0] != "" {
		for _, id := range ids[1:] {
			if id != ids[0] {
				t.Fatalf("concurrent installs minted siblings: %s vs %s", id, ids[0])
			}
		}
	}
}

// A removed record is history: it does not hold the slot, so a fresh install
// gets its own record while the tombstone stays addressable as removed.
func TestOnboardingSlotIgnoresTombstone(t *testing.T) {
	s, auth := NewStore(), aliceAuth()
	d := catalogReadDefinition()
	c := &ControlPlane{Store: s}
	dead, _, err := c.newOnboarding(auth, api.OnboardingCatalog, "gone", d, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	dead.Retire(api.TombstoneRemoved)
	dead.Revision++
	if err := s.PutOnboarding(dead); err != nil {
		t.Fatal(err)
	}
	fresh, attached, err := c.newOnboarding(auth, api.OnboardingCatalog, "fresh", d, "", "", false)
	if err != nil || attached || fresh.OnboardingID == dead.OnboardingID {
		t.Fatalf("tombstone held the slot: attached=%v id=%s err=%v", attached, fresh.OnboardingID, err)
	}
}

// A different definition is a different connector — installs must not collide.
func TestOnboardingSlotScopesToDefinition(t *testing.T) {
	s, auth := NewStore(), aliceAuth()
	d := catalogReadDefinition()
	other := catalogReadDefinition()
	other.DefinitionID = "other-connector"
	c := &ControlPlane{Store: s}
	first, _, err := c.newOnboarding(auth, api.OnboardingCatalog, "a", d, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	second, attached, err := c.newOnboarding(auth, api.OnboardingCatalog, "b", other, "", "", false)
	if err != nil || attached || second.OnboardingID == first.OnboardingID {
		t.Fatalf("definitions shared a slot: attached=%v id=%s err=%v", attached, second.OnboardingID, err)
	}
}
