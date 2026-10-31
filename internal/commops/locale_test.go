package commops

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

// HealthLines renders the caller-scoped block in the owner's language.
func TestHealthLinesLocalized(t *testing.T) {
	recent := time.Now().UTC().Add(-time.Hour)
	e := New(Deps{
		Now:   time.Now,
		Owner: func() (Owner, bool) { return Owner{ID: "alice", Lang: locale.LangES}, true },
		Mappings: func(string) []Mapping {
			return []Mapping{
				{Status: "completed", CreatedAt: recent, TerminalAt: recent.Add(1500 * time.Millisecond)},
				{Status: "uncertain"},
				{Status: "failed", UpdatedAt: recent},
			}
		},
		Restarts:    func(Owner) (int, bool) { return 2, true },
		UsageTotals: func(string, time.Time) (Totals, error) { return Totals{Jobs: 2, InputTokens: 400}, nil },
	})
	got := e.HealthLines(Owner{ID: "alice", Lang: locale.LangES})
	for _, key := range []string{"ops.health-line", "ops.health-latency", "ops.health-jobs", "ops.health-restarts", "ops.health-tokens", "ops.health-sec"} {
		if _, ok := locale.Lookup(locale.LangES, key); !ok {
			t.Errorf("catalog missing es %q", key)
		}
	}
	empty := New(Deps{
		Now:         time.Now,
		Mappings:    func(string) []Mapping { return nil },
		Restarts:    func(Owner) (int, bool) { return 0, false },
		UsageTotals: func(string, time.Time) (Totals, error) { return Totals{}, nil },
	})
	if got := empty.HealthLines(Owner{ID: "alice", Lang: locale.LangES}); !strings.Contains(got, locale.T(locale.LangES, "ops.health-empty")) {
		t.Errorf("empty block %q missing %q", got, locale.T(locale.LangES, "ops.health-empty"))
	}
	if !strings.Contains(got, locale.T(locale.LangES, "ops.health-jobs", 1, 1)) ||
		!strings.Contains(got, locale.T(locale.LangES, "ops.health-restarts", 2)) ||
		!strings.Contains(got, locale.T(locale.LangES, "ops.health-tokens", 200)) {
		t.Errorf("populated block missing translated parts: %q", got)
	}
}

// Record raises the owner notice in the owner's language on a new failure
// class and on recovery.
func TestCanaryNoticeLocalized(t *testing.T) {
	dir := t.TempDir()
	var sent []string
	e := New(Deps{
		Now:       time.Now,
		SpoolRoot: dir,
		Owner:     func() (Owner, bool) { return Owner{ID: "alice", TelegramIDs: []int64{1}, Lang: locale.LangES}, true },
		NotifyOwner: func(ctx context.Context, idempotency string, chatID int64, text string) {
			sent = append(sent, text)
		},
		WriteJSONAtomic: func(path string, v any) error {
			data, err := json.Marshal(v)
			if err != nil {
				return err
			}
			return os.WriteFile(path, data, 0600)
		},
	})
	e.Record(context.Background(), time.Now(), "run_failed")
	e.Record(context.Background(), time.Now(), "")
	if len(sent) != 2 {
		t.Fatalf("notices=%d want 2", len(sent))
	}
	for _, key := range []string{"ops.canary-failing", "ops.canary-recovered"} {
		if _, ok := locale.Lookup(locale.LangES, key); !ok {
			t.Errorf("catalog missing es %q", key)
		}
	}
	if got := sent[0]; got != locale.T(locale.LangES, "ops.canary-failing", "run_failed") {
		t.Errorf("failing notice %q", got)
	}
	if got := sent[1]; got != locale.T(locale.LangES, "ops.canary-recovered", "run_failed") {
		t.Errorf("recovered notice %q", got)
	}
}
