package api

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPreflightArtifactFlows(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	imported := ImportedArtifact{Definition: ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "pf-1", Version: "1.0.0", Transport: ContainerMCP,
		Source: DefinitionSource{Image: "img", Digest: digest, Repository: "https://github.com/acme/x", CommitSHA: strings.Repeat("0", 40)},
	}}
	tools := []ToolSpec{{Name: "ping", Effect: ReadEffect}}

	oldLoad, oldLocal, oldPubList, oldPull := StoredArtifactLoader, LocalMCPToolList, PublishedMCPToolList, PublishedImagePull
	defer func() {
		StoredArtifactLoader, LocalMCPToolList, PublishedMCPToolList, PublishedImagePull = oldLoad, oldLocal, oldPubList, oldPull
	}()

	// happy path: load ok, list answers, contract stamped
	StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) { return "img-id", nil }
	LocalMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return tools, nil }
	got, contract, err := PreflightImportedArtifact(context.Background(), imported, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Definition.Tools) != 1 || contract.Source == "" || len(contract.Tools) != 1 {
		t.Fatalf("preflight: %+v %+v", got.Definition.Tools, contract)
	}
	// loader failure aborts before the probe
	StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) {
		return "", errors.New("no docker")
	}
	if _, _, err := PreflightImportedArtifact(context.Background(), imported, t.TempDir()); err == nil {
		t.Fatal("loader failure continued")
	}
	// probe failure with no named credentials and no promotion → hard error
	StoredArtifactLoader = func(context.Context, string, string, string, int64) (string, error) { return "id", nil }
	LocalMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return nil, errors.New("segfault") }
	if _, _, err := PreflightImportedArtifact(context.Background(), imported, t.TempDir()); err == nil {
		t.Fatal("probe failure admitted")
	}
	// published flow: the entry gate requires a pinned digest + repository and
	// no local archive digest; pull and probe are stubbed
	pub := imported
	pub.Definition.Source = DefinitionSource{Image: "ghcr.io/acme/x", Digest: digest, Repository: "https://github.com/acme/x", CommitSHA: strings.Repeat("0", 40)}
	badPub := pub
	badPub.Definition.Source.ArchiveDigest = "sha256:" + strings.Repeat("44", 32)
	if _, _, err := PreflightPublishedArtifact(context.Background(), badPub); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archive digest on published source: %v", err)
	}
	badPub = pub
	badPub.Definition.Source.Repository = ""
	if _, _, err := PreflightPublishedArtifact(context.Background(), badPub); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no repository: %v", err)
	}
	badPub = pub
	badPub.Definition.Source.Digest = "sha256:short"
	if _, _, err := PreflightPublishedArtifact(context.Background(), badPub); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad digest: %v", err)
	}
	pulled := ""
	PublishedImagePull = func(_ context.Context, image, d string) error { pulled = image + "@" + d; return nil }
	PublishedMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return tools, nil }
	got, contract, err = PreflightPublishedArtifact(context.Background(), pub)
	if err != nil {
		t.Fatal(err)
	}
	if pulled != "ghcr.io/acme/x@"+digest || len(contract.Tools) != 1 {
		t.Fatalf("published: %q %+v", pulled, contract)
	}
	// pull failure propagates
	PublishedImagePull = func(context.Context, string, string) error { return errors.New("pull denied") }
	if _, _, err := PreflightPublishedArtifact(context.Background(), pub); err == nil {
		t.Fatal("pull failure admitted")
	}
}
