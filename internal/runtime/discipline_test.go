package runtime

import (
	"encoding/json"
	"testing"
)

// #196 keeps conversation-session continuity: a new session is an explicit
// owner action (/new, /use), never a hidden per-job or per-attempt rotation.
// Job and retry identity must not leak into the session key, or retries would
// silently fork the conversation and in-loop compaction would stop applying.
func TestSessionStableAcrossJobsAndAttempts(t *testing.T) {
	t.Setenv("HUB_SESSION_REVISION", "test-revision")
	a := validExecuteRequest("alice", "personal", "key-1", "hello")
	a.JobID = "job-1"
	b := a
	b.JobID, b.IdempotencyKey = "job-2", "key-2"
	if sessionIDFor(a) != sessionIDFor(b) {
		t.Fatal("a new job forked the conversation session")
	}
	b = a
	b.IdempotencyKey += ":wake"
	if sessionIDFor(a) != sessionIDFor(b) {
		t.Fatal("a retry attempt forked the conversation session")
	}
	body, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var recovered ExecuteRequest
	if err := json.Unmarshal(body, &recovered); err != nil {
		t.Fatal(err)
	}
	recovered.Text = ""
	if sessionIDFor(a) != sessionIDFor(recovered) {
		t.Fatal("recovery cannot resolve original session")
	}
}
