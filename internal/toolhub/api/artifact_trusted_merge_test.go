package api

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateTrustedArtifactDefinition(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	def := readyDefinition(digest)
	imported := ImportedArtifact{Definition: def}
	if _, err := ApplyPreflightToolList(&imported, []ToolSpec{{Name: "t", Effect: ReadEffect}}); err != nil {
		t.Fatal(err)
	}
	def = imported.Definition
	def.Source.Repository = "https://github.com/acme/demo"
	def.Source.CommitSHA = strings.Repeat("0", 40)
	def.Source.ProvenanceDigest = digest
	def.Source.SBOMDigest = digest
	def.Source.RecipeDigest = digest
	def.Source.ReviewDigest = digest
	def.Source.ArchiveDigest = digest
	if err := ValidateTrustedArtifactDefinition(def); err != nil {
		t.Fatalf("trusted definition: %v", err)
	}
	// evidence digests are mandatory
	for _, drop := range []func(*DefinitionSource){
		func(s *DefinitionSource) { s.ProvenanceDigest = "" },
		func(s *DefinitionSource) { s.SBOMDigest = "" },
		func(s *DefinitionSource) { s.RecipeDigest = "" },
		func(s *DefinitionSource) { s.ReviewDigest = "" },
		func(s *DefinitionSource) { s.Repository = "" },
		func(s *DefinitionSource) { s.CommitSHA = "" },
	} {
		d := def
		drop(&d.Source)
		if err := ValidateTrustedArtifactDefinition(d); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("missing evidence accepted: %v", err)
		}
	}
	// malformed local archive digest is invalid input
	bad := def
	bad.Source.ArchiveDigest = "sha256:short"
	if err := ValidateTrustedArtifactDefinition(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archive digest: %v", err)
	}
	// tool drift past the stamped contract fails the review
	drifted := def
	drifted.Tools = append(drifted.Tools, ToolSpec{Name: "extra", Effect: ReadEffect})
	if err := ValidateTrustedArtifactDefinition(drifted); err == nil {
		t.Fatal("tool drift accepted")
	}
}
