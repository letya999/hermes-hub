package main

import (
	"context"
	"strings"
	"testing"
)

func TestTelegramOwnershipRejectsAnotherRunningStackWithoutExposingToken(t *testing.T) {
	const token = "synthetic-private-token"
	calls := 0
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		switch args[0] {
		case "ps":
			return []byte("own\nforeign\n"), nil
		case "inspect":
			if args[len(args)-1] == "own" {
				return []byte(`[{"Config":{"Labels":{"com.docker.compose.project":"hermes-hub-alice-dev"},"Env":["TELEGRAM_BOT_TOKEN=` + token + `"]}}]`), nil
			}
			return []byte(`[{"Config":{"Labels":{"com.docker.compose.project":"hermes-hub-alice-prod"},"Env":["TELEGRAM_BOT_TOKEN=` + token + `"]}}]`), nil
		}
		return nil, nil
	}
	err := checkTelegramOwnership(context.Background(), "hermes-hub-alice-dev", token, run)
	if err == nil || !strings.Contains(err.Error(), "alice-prod") || strings.Contains(err.Error(), token) {
		t.Fatalf("missing safe conflict diagnostic: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected read-only ps and inspect calls, got %d", calls)
	}
}
