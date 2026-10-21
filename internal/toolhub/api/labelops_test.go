package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// S4.3: Docker label ops query every object kind by ownership selector and
// re-prove the label before a purge — a name collision cannot delete a
// foreign resource.
func TestDockerLabelOpsQueryAndPurgeProof(t *testing.T) {
	var argv [][]string
	run := func(_ context.Context, binary string, args ...string) ([]byte, error) {
		argv = append(argv, append([]string{binary}, args...))
		if len(args) > 1 && args[0] == "ps" {
			return []byte("w-1\nw-2\n"), nil
		}
		if args[0] == "volume" && args[1] == "ls" {
			return []byte("v-1\n"), nil
		}
		if args[0] == "network" && args[1] == "ls" {
			return []byte("net-1\n"), nil
		}
		if args[0] == "images" {
			return []byte("img-1:latest\n"), nil
		}
		if strings.HasSuffix(args[len(args)-1], "foreign") {
			return []byte(`{"app":"other"}`), nil
		}
		return []byte(`{"hermes-hub.connection":"conn-1","hermes-hub.scope":"own-project"}`), nil
	}
	found, err := dockerLabelQuery(context.Background(), run, map[string]string{LabelConnection: "conn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found["container"]) != 2 || len(found["volume"]) != 1 || len(found["network"]) != 1 || len(found["image"]) != 1 {
		t.Fatalf("found=%v", found)
	}
	var sawFilter bool
	for _, cmd := range argv {
		for i, arg := range cmd {
			if arg == "--filter" && i+1 < len(cmd) && cmd[i+1] == "label=hermes-hub.connection=conn-1" {
				sawFilter = true
			}
		}
	}
	if !sawFilter {
		t.Fatalf("no label filter in argv: %v", argv)
	}
	selector := map[string]string{"hermes-hub.scope": "own-project", LabelConnection: "conn-1"}
	if err := dockerLabelPurge(context.Background(), run, "volume", "v-1", selector); err != nil {
		t.Fatal(err)
	}
	if err := dockerLabelPurge(context.Background(), run, "network", "net-1", selector); err != nil {
		t.Fatal(err)
	}
	if err := dockerLabelPurge(context.Background(), run, "volume", "foreign", selector); !errors.Is(err, ErrIsolation) {
		t.Fatalf("foreign purge allowed: %v", err)
	}
	if err := dockerLabelPurge(context.Background(), run, "volume", "v-1", map[string]string{"hermes-hub.scope": "other-project"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("cross-project purge allowed: %v", err)
	}
	if err := dockerLabelPurge(context.Background(), run, "volume", "v-1", map[string]string{"hermes-hub.scope": "own-project"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("connection-free purge allowed: %v", err)
	}
	if err := dockerLabelPurge(context.Background(), run, "volume", "v-1", nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("unscoped purge allowed: %v", err)
	}
	if err := dockerLabelPurge(context.Background(), run, "volume", "v-1", map[string]string{"hermes-hub.scope": ""}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("empty project purge allowed: %v", err)
	}
}

// S4.3: the label scan returns each matching object's full label set so the
// reconciler can diff it against the store; images are never orphan
// candidates and stay out of the result.
func TestDockerLabelScanReturnsLabelsNotImages(t *testing.T) {
	run := func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "ps" {
			return []byte("w-1\nw-2\n"), nil
		}
		if args[0] == "volume" && args[1] == "ls" {
			return []byte("v-1\n"), nil
		}
		if args[0] == "network" && args[1] == "ls" {
			return []byte("net-1\n"), nil
		}
		if args[0] == "images" {
			return []byte("img-1:latest\n"), nil
		}
		return []byte(`{"hermes-hub.connection":"conn-1","hermes-hub.scope":"own-project"}`), nil
	}
	found, err := dockerLabelScan(context.Background(), run, map[string]string{LabelConnection: "conn-1"})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, resource := range found {
		kinds[resource.Kind]++
		if resource.Labels[LabelConnection] != "conn-1" {
			t.Fatalf("scan dropped labels: %+v", resource)
		}
	}
	if kinds["container"] != 2 || kinds["volume"] != 1 || kinds["network"] != 1 {
		t.Fatalf("scan=%v", found)
	}
	if kinds["image"] != 0 {
		t.Fatalf("images scanned as orphan candidates: %v", found)
	}
}
