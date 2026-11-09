package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestEnsureAgentNetworkRejectsPublicAndCreatesMissingInternal(t *testing.T) {
	for _, tc := range []struct {
		name, inspect string
		missing       bool
		wantErr       bool
		wantCalls     int
	}{
		{"existing internal", "true\n", false, false, 1},
		{"public", "false\n", false, true, 1},
		{"missing", "", true, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			run := func(_ context.Context, args ...string) ([]byte, error) {
				calls = append(calls, args)
				if len(calls) == 1 && tc.missing {
					return nil, errors.New("missing")
				}
				return []byte(tc.inspect), nil
			}
			err := ensureAgentNetwork(t.Context(), "hermes-hub-agent-alice-dev", run)
			if (err != nil) != tc.wantErr || len(calls) != tc.wantCalls {
				t.Fatalf("err=%v calls=%v", err, calls)
			}
			if tc.missing && !reflect.DeepEqual(calls[1], []string{"network", "create", "--internal", "hermes-hub-agent-alice-dev"}) {
				t.Fatalf("created wrong network: %v", calls[1])
			}
		})
	}
}
