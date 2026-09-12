package supervisor

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJobsStatusUsesStoredAuthorityAndHidesPrompt(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "known", IdempotencyKey: "known", Text: "PRIVATE_INPUT_CANARY"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	m.finishJob(request, hubruntime.ExecuteResponse{JobID: request.JobID, Text: "final", Status: "completed"}, "completed")
	for _, tc := range []struct {
		method, path, auth string
		status             int
	}{{"GET", "/v1/jobs/known", "secret", 200}, {"GET", "/v1/jobs/known", "other", 401}, {"GET", "/v1/jobs/missing", "secret", 404}, {"POST", "/v1/jobs/known", "secret", 405}, {"GET", "/v1/jobs/known/other", "secret", 400}, {"GET", "/v1/jobs", "secret", 405}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.auth)
		response := httptest.NewRecorder()
		m.Handler().ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, response.Code)
		}
		if strings.Contains(response.Body.String(), "PRIVATE_INPUT_CANARY") {
			t.Fatal("prompt disclosed")
		}
		if response.Code == 200 {
			var payload struct {
				Identity identity.Envelope `json:"identity"`
			}
			if json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Identity != request.Envelope {
				t.Fatal("stored audience lost")
			}
		}
	}
}
