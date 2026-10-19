package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type Rank string

const (
	RankL0  Rank = "L0"
	RankL1  Rank = "L1"
	RankL2  Rank = "L2"
	RankL3  Rank = "L3"
	RankL4  Rank = "L4"
	RankL5  Rank = "L5"
	RankL6  Rank = "L6"
	RankLH  Rank = "hint"
	RankR0  Rank = "R0"
	RankR1  Rank = "R1"
	RankR2  Rank = "R2"
	RankR3  Rank = "R3"
	RankR4  Rank = "R4"
	RankR5  Rank = "R5"
	RankR6  Rank = "R6"
	RankR7  Rank = "R7"
	RankPRM Rank = "PRM"
)

type TrustTier string

const (
	TrustT0 TrustTier = "T0"
	TrustT1 TrustTier = "T1"
	TrustT2 TrustTier = "T2"
)

type SubjectKind string

const (
	SubjectContainerMCP SubjectKind = "mcp-container"
	SubjectRemoteMCP    SubjectKind = "mcp-remote"
	SubjectCLI          SubjectKind = "cli"
)

type RecipeSubject struct {
	Kind SubjectKind `json:"kind"`
	Ref  string      `json:"ref"`
}

type RecipeSurface struct {
	ToolsDigest    string   `json:"tools_digest,omitempty"`
	Effects        []string `json:"effects,omitempty"`
	Toolsets       []string `json:"toolsets,omitempty"`
	DefaultEnabled []string `json:"default_enabled,omitempty"`
	ProbeTool      string   `json:"probe_tool,omitempty"`
}

type Recipe struct {
	Subject     RecipeSubject    `json:"subject"`
	Launch      LaunchRecipe     `json:"launch"`
	Credentials ConnectionRecipe `json:"credentials"`
	Surface     RecipeSurface    `json:"surface,omitempty"`
	Egress      []string         `json:"egress,omitempty"`
	State       string           `json:"state,omitempty"`
	Shareable   string           `json:"shareable,omitempty"`
	Trust       TrustTier        `json:"trust"`
}

// Validate enforces the recipe contract: a recipe is a shareable plan and
// may never embed credential material. Static environment carrying a
// token-shaped literal is a leak, not configuration.
func (r Recipe) Validate() error {
	for key, value := range r.Launch.Environment {
		if secretValuePattern.MatchString(value) {
			return fmt.Errorf("%w: recipe environment %s carries credential material", ErrInvalid, key)
		}
	}
	return nil
}

// Digest content-addresses a recipe: the canonical encoding sorts every
// order-insensitive slice before hashing, so the same claims in any input
// order yield the same digest.
func (r Recipe) Digest() string {
	canonical := r
	canonical.Credentials.Fields = slices.Clone(r.Credentials.Fields)
	slices.SortFunc(canonical.Credentials.Fields, func(a, b ConnectionField) int { return strings.Compare(a.Name, b.Name) })
	canonical.Launch.Mounts = slices.Clone(r.Launch.Mounts)
	slices.SortFunc(canonical.Launch.Mounts, func(a, b Mount) int {
		if c := strings.Compare(a.Target, b.Target); c != 0 {
			return c
		}
		return strings.Compare(a.Source, b.Source)
	})
	canonical.Egress = slices.Clone(r.Egress)
	slices.Sort(canonical.Egress)
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
