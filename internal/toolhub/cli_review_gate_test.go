package toolhub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerReviewerRejectsUnpinnedSourceBeforeFetch(t *testing.T) {
	dir := t.TempDir()
	seccomp := filepath.Join(dir, "seccomp.json")
	source := ArtifactSource{Repository: "https://github.com/owner/repo", CommitSHA: strings.Repeat("a", 40)}
	for _, reviewer := range []SourceReviewer{DefaultSourceReviewer("relative", seccomp), DefaultSourceReviewer(dir, seccomp)} {
		if _, err := reviewer(context.Background(), source, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("missing local review prerequisite accepted: %v", err)
		}
	}
	if err := os.WriteFile(seccomp, []byte(`{"defaultAction":"SCMP_ACT_ERRNO"}`), 0600); err != nil {
		t.Fatal(err)
	}
	reviewer := DefaultSourceReviewer(dir, seccomp)
	if _, err := reviewer(context.Background(), source, &RecipeCandidate{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unproven registry selection accepted: %v", err)
	}
	for _, bad := range []ArtifactSource{{Repository: "https://attacker.invalid/owner/repo", CommitSHA: source.CommitSHA}, {Repository: source.Repository, CommitSHA: "main"}} {
		if _, err := reviewer(context.Background(), bad, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unreviewed source fetch permitted: %v", err)
		}
	}
}

func TestCLIReviewKeepsCredentialGatePending(t *testing.T) {
	imported := ImportedArtifact{Definition: CLICatalogDefinitions()[1]}
	gate := &CredentialGate{Names: []string{"SERVICE_TOKEN"}, Groups: [][]string{{"SERVICE_TOKEN"}}}
	review, ok := reviewFromCredentialGate(imported, gate, nil)
	if !ok || !review.AdmissionPending || review.Definition.DefinitionID != imported.Definition.DefinitionID || len(review.AdmissionGroups) != 1 || review.AdmissionDetail != gate.Error() {
		t.Fatalf("credential gate lost: %+v %v", review, ok)
	}
	gate.Detail = "protected credential form required"
	review, ok = reviewFromCredentialGate(imported, gate, nil)
	if !ok || review.AdmissionDetail != gate.Detail {
		t.Fatal("gate detail lost")
	}
	for _, err := range []error{nil, errors.New("not a credential rejection")} {
		if _, ok := reviewFromCredentialGate(imported, err, nil); ok {
			t.Fatal("unrelated error treated as credential readiness")
		}
	}
	if _, ok := reviewFromCredentialGate(ImportedArtifact{}, gate, nil); ok {
		t.Fatal("unreviewed empty definition admitted")
	}
}
