package agenttools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutineDestinationArgumentsReachGatewayAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		op, input, key, value string
		reject                bool
	}{
		{"routine_create", `{"id":"x","origin_job_id":"run-a"}`, "origin_job_id", "run-a", false},
		{"routine_create", `{"id":"x","destination_task_id":"default"}`, "destination_task_id", "default", false},
		{"routine_create", `{"id":"x"}`, "", "", true},
		{"routine_list", `{"view":"destinations","query":"Работа","cursor":"a","limit":2}`, "view", "destinations", false},
		{"routine_list", `{"view":"typo"}`, "", "", true},
		{"routine_edit", `{"id":"x","action":"update","expected_revision":2,"origin_job_id":"run-a","patch":{"destination_task_id":"current"}}`, "origin_job_id", "run-a", false},
	} {
		t.Run(tc.op+tc.input, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.op == "routine_list" {
					q := r.URL.Query()
					if q.Get("view") != "destinations" || q.Get("query") != "Работа" || q.Get("after") != "a" || q.Get("limit") != "2" {
						t.Errorf("discovery query lost: %v", q)
					}
				} else {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body[tc.key] != tc.value {
						t.Errorf("routing field lost: %v", body)
					}
					if tc.op == "routine_edit" && (body["destination_task_id"] != "current" || body["expected_revision"] != float64(2)) {
						t.Errorf("edit lost: %v", body)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			}))
			defer server.Close()
			v := fixture(t)
			v.CommunicationURL = server.URL
			var input Input
			if err := json.Unmarshal([]byte(tc.input), &input); err != nil {
				t.Fatal(err)
			}
			_, err := v.Routine(context.Background(), tc.op, input)
			if tc.reject {
				if err == nil || calls != 0 {
					t.Errorf("invalid destination accepted: calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Errorf("routing request failed: %v calls=%d", err, calls)
			}
			if tc.op == "routine_edit" {
				if _, mutated := input.Patch["origin_job_id"]; mutated {
					t.Error("caller patch mutated")
				}
			}
		})
	}
}
