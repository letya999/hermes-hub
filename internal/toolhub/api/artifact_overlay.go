package api

import ()

// ArtifactOverlay is a reviewed second source merged under a clean prefix.
type ArtifactOverlay struct {
	Source ArtifactSource `json:"source"`
	Into   string         `json:"into"`
}
