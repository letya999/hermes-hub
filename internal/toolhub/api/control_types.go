package api

import (
	"context"
)

type SourceReview struct {
	Definition       ToolDefinition
	Permissions      []string
	Effects          []string
	ReviewDigest     string
	Recipe           *RecipeResolution
	AdmissionPending bool
	AdmissionDetail  string
	AdmissionGroups  [][]string
}

type SourceReviewer func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error)
type SourceResolver func(context.Context, string) (ArtifactSource, error)
