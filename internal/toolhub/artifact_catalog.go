package toolhub

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// RegisterTrustedArtifact is the only publication step. The review digest is
// recomputed from all immutable inputs, so changing source/artifact/contract
// cannot silently reuse a previous approval.
func (s *Store) RegisterTrustedArtifact(imported api.ImportedArtifact, approvedReviewDigest string) error {
	if s == nil || imported.Definition.Transport != api.ContainerMCP || approvedReviewDigest == "" || imported.Definition.Source.ReviewDigest != approvedReviewDigest {
		return fmt.Errorf("%w: artifact review approval required", api.ErrUnauthorized)
	}
	if expected, err := api.ReviewDigestForImported(imported); err != nil || expected != approvedReviewDigest {
		return fmt.Errorf("%w: artifact review packet drift", api.ErrStale)
	}
	if err := api.ValidateTrustedArtifactDefinition(imported.Definition); err != nil {
		return err
	}
	if imported.Definition.Source.Digest != imported.Artifact.Evidence.ImageManifestDigest || imported.Definition.Source.ArchiveDigest != imported.Artifact.ArchiveDigest || imported.Definition.Source.ProvenanceDigest != imported.Artifact.Evidence.ProvenanceDigest || imported.Definition.Source.SBOMDigest != imported.Artifact.Evidence.SBOMDigest {
		return fmt.Errorf("%w: artifact evidence does not match definition", api.ErrStale)
	}
	entrypoint := append([]string{imported.Definition.Source.Command}, imported.Definition.Source.Args...)
	if len(imported.Definition.Source.Command) == 0 {
		entrypoint = nil
	}
	if len(entrypoint) != len(imported.Recipe.Entrypoint) {
		return fmt.Errorf("%w: artifact entrypoint drift", api.ErrStale)
	}
	for i := range entrypoint {
		if entrypoint[i] != imported.Recipe.Entrypoint[i] {
			return fmt.Errorf("%w: artifact entrypoint drift", api.ErrStale)
		}
	}
	return s.RegisterDefinition(imported.Definition)
}
