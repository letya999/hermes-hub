package main

import (
	"context"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
)

func TestTelegramOwnershipCheckAppliesOnlyToGatewaySpaces(t *testing.T) {
	// Member spaces declare ingress:telegram but deliver through the org
	// gateway — running the ownership check for them always conflicts with
	// the org's live communication-hub. Only spaces that render a gateway
	// may claim the bot token.
	infraTrue := true
	infraFalse := false
	member := stack.Settings{Infra: &infraFalse, Ingress: []string{"telegram"}}
	infra := stack.Settings{Infra: &infraTrue, Ingress: []string{"telegram"}}
	plain := stack.Settings{Infra: &infraTrue}
	if telegramOwnershipCheckApplies(member) {
		t.Fatal("member space must not run the bot-ownership check")
	}
	if !telegramOwnershipCheckApplies(infra) {
		t.Fatal("infra space with telegram must run the bot-ownership check")
	}
	if telegramOwnershipCheckApplies(plain) {
		t.Fatal("infra space without telegram has no bot token to check")
	}
}

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
