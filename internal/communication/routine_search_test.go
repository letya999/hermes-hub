package communication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutineSearchFiltersBeforePagingInsideOwnerScope(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	for _, row := range []struct{ id, text, owner string }{
		{"a-no-match", "other", "alice"}, {"b-friday", "FRIDAY report", "alice"},
		{"c-friday", "private Friday", "bob"}, {"d-friday", "other", "alice"},
	} {
		owner := env
		owner.PrincipalID = row.owner
		owner.ContextID = row.owner
		in := Schedule{ScheduleID: row.id, Envelope: owner, UserID: row.owner, ActorID: row.owner, ScopeID: "user:" + row.owner, Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: row.text, JobKind: "reminder"}
		if _, err := g.spool.CreateSchedule(in, owner, ""); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	for _, want := range []string{"b-friday", "d-friday"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/routines?query=Friday&limit=1&kind=reminder&state=active&after="+cursor, nil)
		r.Header.Set("Authorization", "Bearer control-token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		var page RoutinePage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatalf("query page=%s want=%s", w.Body.String(), want)
		}
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatal("unexpected extra page", cursor)
	}
}
