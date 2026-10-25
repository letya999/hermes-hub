package commodel

import (
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestJobEpochIsImmutableAdmissionMetadata(t *testing.T) {
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job", IdempotencyKey: "key", Text: "hello", OperationalEpoch: 2}
	mapping := MappingFromJob(job, time.Unix(100, 0))
	if !mapping.MatchesJob(job) {
		t.Fatal("admitted job does not match its mapping")
	}
	changed := job
	changed.OperationalEpoch = 4
	if JobFingerprint(changed) == JobFingerprint(job) {
		t.Error("epoch change can reuse admission fingerprint")
	}
	if mapping.MatchesJob(changed) {
		t.Error("mapping accepts a different admission epoch")
	}
	mapping.OperationalEpoch = 4
	if mapping.MatchesJob(job) {
		t.Error("tampered mapping epoch accepted")
	}
}
