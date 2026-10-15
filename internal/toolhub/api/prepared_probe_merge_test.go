package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// StartPreparedProbeProxyWithDocker wires an internal network, a config
// volume, a read-only proxy container, a bridge uplink and a private v4
// address; every docker failure tears down the partial resources.
func TestStartPreparedProbeProxyContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var calls []string
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "inspect" {
			return []byte("172.20.0.2\n"), nil
		}
		return nil, nil
	}
	network, proxyURL, stop, err := StartPreparedProbeProxyWithDocker(ctx, []string{"api.example"}, run)
	if err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	if proxyURL != "http://172.20.0.2:3128" || !strings.HasSuffix(network, "-net") {
		t.Fatalf("outputs: %q %q", proxyURL, network)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"network create", "--internal", "volume create", "--cap-drop", "no-new-privileges", "cp ", "network connect", "start "} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "hermes-hub.role=artifact-preflight") {
		t.Fatal("probe resources must carry the preflight label")
	}
	calls = nil
	stop()
	if !strings.Contains(strings.Join(calls, "\n"), "rm -f") {
		t.Fatal("stop must remove the proxy")
	}

	// Failure at each step surfaces ErrIsolation and stops cleanly.
	steps := []string{"network", "volume", "create", "cp", "connect", "start", "inspect"}
	for _, step := range steps {
		failing := func(ctx context.Context, args ...string) ([]byte, error) {
			for _, a := range args {
				if a == step {
					return nil, errors.New("nope")
				}
			}
			if args[0] == "inspect" {
				return []byte("172.20.0.2"), nil
			}
			return nil, nil
		}
		if _, _, _, err := StartPreparedProbeProxyWithDocker(ctx, []string{"api.example"}, failing); !errors.Is(err, ErrIsolation) {
			t.Fatalf("failure at %q: %v", step, err)
		}
	}
	// A non-private or missing proxy address is refused.
	badIP := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return []byte("8.8.8.8"), nil
		}
		return nil, nil
	}
	if _, _, _, err := StartPreparedProbeProxyWithDocker(ctx, []string{"api.example"}, badIP); !errors.Is(err, ErrIsolation) {
		t.Fatalf("public proxy address must refuse: %v", err)
	}
	// Empty egress cannot form a proxy config.
	if _, _, _, err := StartPreparedProbeProxyWithDocker(ctx, nil, run); err == nil {
		t.Fatal("empty egress must error")
	}
}
