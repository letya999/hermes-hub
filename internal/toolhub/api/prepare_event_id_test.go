package api

import "testing"

func TestPrepareEventIDMatchesGenerator(t *testing.T) {
	for range 100 {
		if id := RandomNonce(); !ValidPrepareEventID(id) {
			t.Fatalf("generated event ID rejected: length=%d", len(id))
		}
	}
	for _, id := range []string{"", "not-a-nonce", "g123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcde"} {
		if ValidPrepareEventID(id) {
			t.Fatalf("invalid event ID accepted: length=%d", len(id))
		}
	}
}
