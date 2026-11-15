package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDockerLabelQueryAndScan(t *testing.T) {
	ctx := context.Background()
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "ps "):
			return []byte("rt-one\nrt-two\n"), nil
		case strings.HasPrefix(joined, "volume ls"):
			return []byte("vol-one\n"), nil
		case strings.HasPrefix(joined, "network ls"):
			return []byte(""), nil
		case strings.HasPrefix(joined, "images"):
			return []byte("img:local\n"), nil
		case strings.HasPrefix(joined, "inspect"):
			return []byte(`{"hermes-hub.connection":"conn-1","other":"x"}`), nil
		case strings.Contains(joined, "inspect"):
			return []byte(`{"hermes-hub.connection":"conn-1"}`), nil
		}
		return nil, errors.New("unexpected docker call: " + joined)
	}
	found, err := dockerLabelQuery(ctx, run, map[string]string{LabelConnection: "conn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found["container"]) != 2 || len(found["volume"]) != 1 || len(found["image"]) != 1 {
		t.Fatalf("query: %v", found)
	}
	if _, ok := found["network"]; ok {
		t.Fatal("empty network list should not appear")
	}
	// Query errors propagate with the kind annotated.
	failing := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("daemon down")
	}
	if _, err := dockerLabelQuery(ctx, failing, map[string]string{"k": ""}); err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatalf("query error: %v", err)
	}
	// Scan re-inspects each discovered object; images are skipped by design.
	resources, err := dockerLabelScan(ctx, run, map[string]string{LabelConnection: "conn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 3 {
		t.Fatalf("scan resources: %v", resources)
	}
	for _, res := range resources {
		if res.Labels[LabelConnection] != "conn-1" {
			t.Fatalf("labels missing on %s/%s", res.Kind, res.Name)
		}
	}
	if _, err := dockerLabelScan(ctx, failing, map[string]string{LabelConnection: "conn-1"}); err == nil {
		t.Fatal("scan swallowed query error")
	}
}

func TestDockerLabelPurgeOwnership(t *testing.T) {
	ctx := context.Background()
	ownerLabels := `{"hermes-hub.connection":"conn-1","extra":"1"}`
	var removed []string
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "inspect") {
			return []byte(ownerLabels), nil
		}
		removed = append(removed, joined)
		return nil, nil
	}
	selector := map[string]string{LabelConnection: "conn-1"}
	for _, kind := range []string{"container", "volume", "network"} {
		if err := dockerLabelPurge(ctx, run, kind, "obj", selector); err != nil {
			t.Fatalf("purge %s: %v", kind, err)
		}
	}
	if len(removed) != 3 {
		t.Fatalf("removals: %v", removed)
	}
	if err := dockerLabelPurge(ctx, run, "image", "img", selector); err == nil {
		t.Fatal("image purge admitted")
	}
	// Purge refuses objects whose labels fail to re-prove ownership.
	foreign := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"hermes-hub.connection":"conn-other"}`), nil
	}
	if err := dockerLabelPurge(ctx, foreign, "container", "obj", selector); err == nil {
		t.Fatal("foreign object purge admitted")
	}
	empty := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`null`), nil
	}
	if err := dockerLabelPurge(ctx, empty, "volume", "obj", selector); err == nil {
		t.Fatal("unowned object purge admitted")
	}
	if err := dockerLabelPurge(ctx, run, "container", "obj", map[string]string{"k": "v"}); err == nil {
		t.Fatal("selector without ownership admitted")
	}
	failing := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("daemon down")
	}
	if err := dockerLabelPurge(ctx, failing, "container", "obj", selector); err == nil {
		t.Fatal("inspect failure swallowed")
	}
}

func TestLabelSelectorHelpers(t *testing.T) {
	if got := labelSelectorValue(""); got != "" {
		t.Fatalf("empty value: %q", got)
	}
	if got := labelSelectorValue("v"); got != "=v" {
		t.Fatalf("value: %q", got)
	}
	args := labelFilterArgs([]string{"a", "b"})
	if len(args) != 4 || args[0] != "--filter" || args[1] != "a" || args[3] != "b" {
		t.Fatalf("filter args: %v", args)
	}
	for kind, want := range map[string]string{"container": "inspect", "volume": "volume inspect", "network": "network inspect"} {
		got := strings.Join(dockerInspectLabelsArgs(kind, "x"), " ")
		if !strings.HasPrefix(got, want) {
			t.Fatalf("%s inspect args: %q", kind, got)
		}
	}
	if dockerInspectLabelsArgs("image", "x") != nil {
		t.Fatal("image inspect args should be nil")
	}
}
