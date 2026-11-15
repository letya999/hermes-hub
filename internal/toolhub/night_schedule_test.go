package toolhub

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passportview"
)

func TestNightScheduleRequiresActivatedPassport(t *testing.T) {
	for _, tc := range []struct {
		name, revision string
		pending, allow bool
	}{
		{name: "active", revision: "r-0123456789abcdef", allow: true},
		{name: "mismatched profile", revision: "r-1111111111111111"},
		{name: "missing profile revision"},
		{name: "interrupted apply", revision: "r-0123456789abcdef", pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, auth, _, profile := managedStore(t)
			root := t.TempDir()
			writeMemoryPassportEnv(t, root, auth.PrincipalID, auth.Environment, true)
			profile.Revision++
			profile.PassportRevision = tc.revision
			if err := putProfile(t, store, profile); err != nil {
				t.Fatal(err)
			}
			if tc.pending {
				if err := os.WriteFile(filepath.Join(root, auth.PrincipalID, "passports", passportview.ApplyJournalFile), []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e := &projectionEndpoint{gateway: &Gateway{Store: store, Tokens: map[string]identity.Envelope{"synthetic": auth}, PassportRoot: root}}
			plan, err := e.NightSchedule(auth)
			if tc.allow {
				if err != nil || !plan.Enabled || plan.Revision != tc.revision {
					t.Fatalf("active clock denied: %+v %v", plan, err)
				}
			} else if err == nil {
				t.Fatal("dreaming accepted an inactive or mixed passport generation")
			}
		})
	}
}
