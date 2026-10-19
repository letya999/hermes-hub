package api

import (
	"context"
	"errors"
	"testing"
)

func TestCLIPackageImportValidation(t *testing.T) {
	src := ArtifactSource{PackageRegistry: "pypi", PackageName: "requests", PackageVersion: "1.0"}
	cfg := NormalizeArtifactImportConfig(ArtifactImportConfig{DefinitionID: "pkg-1", Entrypoint: []string{"/tool"}})
	ctx := context.Background()

	// validation gates run before any registry fetch or build
	bad := src
	bad.PackageVersion = ""
	if _, err := ImportCLIPackageArtifact(ctx, bad, cfg, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no version: %v", err)
	}
	bad.PackageVersion = "1.0"
	bad.PackageName = "name with space"
	if _, err := ImportCLIPackageArtifact(ctx, bad, cfg, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("name: %v", err)
	}
	badImg := cfg
	badImg.Image = "name@sha256:x"
	if _, err := ImportCLIPackageArtifact(ctx, src, badImg, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutable image: %v", err)
	}
	badBase := cfg
	badBase.BaseImage = "alpine:latest"
	if _, err := ImportCLIPackageArtifact(ctx, src, badBase, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("base image on package: %v", err)
	}
	badEntry := cfg
	badEntry.Entrypoint = []string{"/a", "/b"}
	if _, err := ImportCLIPackageArtifact(ctx, src, badEntry, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multi entrypoint: %v", err)
	}
	// unknown registry dies at version resolution
	unknown := src
	unknown.PackageRegistry = "crates"
	if _, err := ImportCLIPackageArtifact(ctx, unknown, cfg, RestrictedBuildConfig{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("registry: %v", err)
	}
}
