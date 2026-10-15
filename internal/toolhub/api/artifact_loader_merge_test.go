package api

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// LoadStoredOCIArtifactWith walks the docker contract: local npipe/unix
// context, Linux daemon, load output parsing, tag, and image inspect.
func TestLoadStoredOCIArtifactContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var calls []string
	run := func(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "context":
			return []byte("npipe:////./pipe/docker_engine\n"), nil
		case "info":
			return []byte("linux\n"), nil
		case "load":
			return []byte("Loaded image ID: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"), nil
		case "tag":
			return nil, nil
		case "image":
			return []byte("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	id, err := LoadStoredOCIArtifactWith(ctx, strings.NewReader("tar-bytes"), "repo/img:1", run)
	if err != nil || id != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("load: %v %q", err, id)
	}
	if len(calls) != 5 || !strings.HasPrefix(calls[2], "load") {
		t.Fatalf("docker sequence: %v", calls)
	}

	failAt := func(step string) func(context.Context, io.Reader, ...string) ([]byte, error) {
		return func(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
			if args[0] == step {
				return nil, errors.New("nope")
			}
			return run(ctx, input, args...)
		}
	}
	for _, step := range []string{"context", "info", "load", "tag", "image"} {
		if _, err := LoadStoredOCIArtifactWith(ctx, strings.NewReader("x"), "repo/img:1", failAt(step)); err == nil {
			t.Fatalf("failure at %s accepted", step)
		}
	}
	// Non-local context and non-Linux daemon are isolation faults.
	remote := func(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
		return []byte("tcp://remote:2375"), nil
	}
	if _, err := LoadStoredOCIArtifactWith(ctx, strings.NewReader("x"), "repo/img:1", remote); !errors.Is(err, ErrIsolation) {
		t.Fatalf("remote context: %v", err)
	}
	if _, err := LoadStoredOCIArtifactWith(ctx, nil, "repo/img:1", run); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil artifact: %v", err)
	}
	if _, err := LoadStoredOCIArtifactReader(ctx, nil, "repo/img:1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reader nil artifact: %v", err)
	}
}
