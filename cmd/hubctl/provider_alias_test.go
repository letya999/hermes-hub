package main

import (
	"context"
	"strings"
	"testing"
)

func TestSharedCLIProxyAliasRejectsAnotherInfraOwner(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("foreign\n"), nil
		}
		return []byte(`[{"Config":{"Labels":{"com.docker.compose.project":"hermes-hub-other-dev"}},"NetworkSettings":{"Networks":{"hermes-hub-runtime":{}}}}]`), nil
	}
	err := checkProviderAliasOwnership(context.Background(), "hermes-hub-local-dev", run)
	if err == nil || !strings.Contains(err.Error(), "hermes-hub-other-dev") {
		t.Fatalf("shared provider alias conflict accepted: %v", err)
	}
}
