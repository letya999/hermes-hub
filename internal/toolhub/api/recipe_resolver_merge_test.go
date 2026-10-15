package api

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"testing"
)

func tarFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Size: int64(len(body)), Typeflag: tar.TypeReg, Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadRecipeContextContract(t *testing.T) {
	files, err := ReadRecipeContext(tarFixture(t, map[string]string{
		"server.json": "{}", "docs/readme.md": "hi",
	}), 1<<20)
	if err != nil || len(files) != 2 {
		t.Fatalf("files: %v %v", files, err)
	}
	// bounds
	if _, err := ReadRecipeContext([]byte("x"), 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero max: %v", err)
	}
	if _, err := ReadRecipeContext(tarFixture(t, map[string]string{"a": "b"}), 1<<30); !errors.Is(err, ErrInvalid) {
		t.Fatalf("huge max: %v", err)
	}
	if _, err := ReadRecipeContext([]byte("not a tar at all padded to look big enough"), 1<<20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed tar: %v", err)
	}
	// unsafe entries: traversal path, non-clean path, symlink, duplicate, oversize file
	oversized := tarFixture(t, map[string]string{"big": "0123456789"})
	if _, err := ReadRecipeContext(oversized, 4); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize file accepted")
	}
	var dirBuf bytes.Buffer
	dw := tar.NewWriter(&dirBuf)
	for _, h := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644},
		{Name: "dir", Typeflag: tar.TypeDir, Mode: 0o755},
	} {
		dw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			dw.Write([]byte("x"))
		}
	}
	dw.Close()
	if _, err := ReadRecipeContext(dirBuf.Bytes(), 1<<20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe entries accepted")
	}
}

type stubCatalog struct {
	candidates []RecipeCandidate
	err        error
}

func (s stubCatalog) Lookup(context.Context, RecipeLookup) ([]RecipeCandidate, error) {
	return s.candidates, s.err
}

func TestRecipeResolverResolveContract(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	source := ArtifactSource{Repository: "https://github.com/acme/demo", CommitSHA: sha}
	mcpJSON := `{"mcpServers":{"remote":{"url":"https://mcp.example.io/sse","env":{"API_TOKEN":"x"}}}}`
	ctxData := tarFixture(t, map[string]string{".mcp.json": mcpJSON})

	matching := RecipeCandidate{Repository: "https://github.com/acme/demo", CommitSHA: sha,
		Launch:     LaunchRecipe{Transport: RemoteMCP, Endpoint: "https://mcp.example.io/sse"},
		Evidence:   []RecipeEvidence{{Source: "catalog", Detail: "pinned"}},
		Connection: ConnectionRecipe{Fields: []ConnectionField{{Name: "API_TOKEN", Required: true}}}}
	stranger := RecipeCandidate{Repository: "https://github.com/other/repo", CommitSHA: sha}

	resolver := RecipeResolver{Catalogs: []RecipeCatalog{
		stubCatalog{candidates: []RecipeCandidate{matching}},
		stubCatalog{candidates: []RecipeCandidate{stranger}},
		stubCatalog{err: errors.New("catalog offline")},
		nil,
	}}
	res, err := resolver.Resolve(context.Background(), source, ctxData)
	if err != nil {
		t.Fatal(err)
	}
	if res.Launch.Endpoint != "https://mcp.example.io/sse" {
		t.Fatalf("launch: %+v", res.Launch)
	}
	if res.State != "ready" {
		t.Fatalf("state %q warnings %v", res.State, res.Warnings)
	}
	if len(res.Evidence) == 0 || len(res.Connection.Fields) != 1 {
		t.Fatalf("evidence/connection: %+v", res)
	}
	found := false
	for _, w := range res.Warnings {
		if w == "catalog candidate ignored: repository or commit did not match the pinned source" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mismatch warning missing: %v", res.Warnings)
	}
}
