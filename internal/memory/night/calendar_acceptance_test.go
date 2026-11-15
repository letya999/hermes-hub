package night

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// The acceptance calendar uses the production capture writer and a supplied
// clock in temporary vaults. It never changes system time or a personal vault.
func TestDreamingCalendarTwoOwnersThreeDaysTenFacts(t *testing.T) {
	for _, owner := range []string{"alice", "bob"} {
		root := filepath.Join(t.TempDir(), owner)
		if err := vault.Ensure(root); err != nil {
			t.Fatal(err)
		}
		daily, err := vault.OpenDaily(root, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		for day := 1; day <= 3; day++ {
			when := time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC)
			facts := make([]struct{ Card, Fact, Quote string }, 0, 10)
			lines := make([]string, 0, 10)
			for n := 1; n <= 10; n++ {
				body := fmt.Sprintf("I choose %s day %d fact %d", owner, day, n)
				lines = append(lines, body)
				facts = append(facts, struct{ Card, Fact, Quote string }{"notes/calendar", body, body})
			}
			if err := daily.Append(memory.Entry{ID: fmt.Sprintf("%s-%d", owner, day), Kind: memory.KindInteractive,
				Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: strings.Join(lines, "\n"), ObservedAt: when}, "telegram_bot"); err != nil {
				t.Fatal(err)
			}
			proposal, _ := json.Marshal(map[string]any{"summary": fmt.Sprintf("%s day %d", owner, day), "facts": facts})
			res, err := Run(context.Background(), root, when, Options{Now: when.Add(12 * time.Hour), TZ: time.UTC,
				Relay: func(_ context.Context, _, prompt string) (string, error) {
					for _, f := range facts {
						if !strings.Contains(prompt, f.Quote) {
							t.Fatalf("%s %d missing captured fact %q", owner, day, f.Quote)
						}
					}
					return string(proposal), nil
				}})
			if err != nil || res.Facts != 10 || !DayComplete(root, when.Format("2006-01-02")) {
				t.Fatalf("%s day %d: facts=%d complete=%t err=%v", owner, day, res.Facts, DayComplete(root, when.Format("2006-01-02")), err)
			}
		}
	}
}
