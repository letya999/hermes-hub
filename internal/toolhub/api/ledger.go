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

// Claim is one typed assertion a source made: either a credential-field
// declaration (Field set) or a launch-attribute declaration (Attribute set).
// Method names a credential method (pat, oauth, github-app) the claim
// contributes to.
type Claim struct {
	Field     string `json:"field,omitempty"`
	Required  *bool  `json:"required,omitempty"`
	Secret    *bool  `json:"secret,omitempty"`
	Delivery  string `json:"delivery,omitempty"`
	Target    string `json:"target,omitempty"`
	Attribute string `json:"attribute,omitempty"`
	Value     string `json:"value,omitempty"` // canonical JSON of the attribute value
	Method    string `json:"method,omitempty"`
}

// LedgerEntry records one external claim for reproducible re-resolution:
// the source, its ladder rank, what was verified, the response digest and
// when it was fetched. Ignored entries are kept — a catalog candidate that
// did not match the pinned source is evidence of the decision, not noise.
type LedgerEntry struct {
	Claim     Claim  `json:"claim"`
	Source    string `json:"source"`
	Ref       string `json:"ref,omitempty"`
	Rank      Rank   `json:"rank"`
	Verified  string `json:"verified,omitempty"`
	Ignored   bool   `json:"ignored,omitempty"`
	Digest    string `json:"digest,omitempty"`
	FetchedAt string `json:"fetched_at,omitempty"`
}

// ErrAmbiguousCredentialContract fails a resolution where two same-rank
// sources disagree about one credential field's contract.
var ErrAmbiguousCredentialContract = fmt.Errorf("%w: ambiguous credential contract", ErrInvalid)

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
func launchRankOrder(r Rank) int {
	switch r {
	case RankL0:
		return 0
	case RankL3:
		return 3
	case RankL4:
		return 4
	case RankLH:
		return 7
	}
	return 99
}

func credRankOrder(r Rank) int {
	switch r {
	case RankR0:
		return 0
	case RankR1:
		return 1
	case RankR2:
		return 2
	case RankR3:
		return 3
	case RankR4:
		return 4
	case RankR6:
		return 6
	}
	return 99
}

// ResolveLedger turns a claim ledger into a recipe. It is a pure function:
// no network, no clock — identical ledgers yield identical digests. The
// highest rank wins per launch attribute and per credential field; two
// disagreeing claims at the same top rank fail closed. Heuristic ranks
// (R6/R7) demote required to a suggestion and mark the field unverified.
func ResolveLedger(subject RecipeSubject, entries []LedgerEntry) (Recipe, error) {
	recipe := Recipe{Subject: subject}
	launch := map[string][]LedgerEntry{}
	fields := map[string][]LedgerEntry{}
	for _, entry := range entries {
		if entry.Ignored {
			continue
		}
		switch {
		case entry.Claim.Attribute != "":
			launch[entry.Claim.Attribute] = append(launch[entry.Claim.Attribute], entry)
		case entry.Claim.Field != "":
			fields[entry.Claim.Field] = append(fields[entry.Claim.Field], entry)
		}
	}
	worstLaunch, worstCred := 0, 0
	for _, attribute := range sortedKeys(launch) {
		winner, err := topRank(launch[attribute], launchRankOrder)
		if err != nil {
			return Recipe{}, fmt.Errorf("%w: %s (%s)", ErrAmbiguousEntrypoint, attribute, err)
		}
		if err := applyLaunchClaim(&recipe.Launch, attribute, winner.Claim.Value); err != nil {
			return Recipe{}, err
		}
		if o := launchRankOrder(winner.Rank); o > worstLaunch {
			worstLaunch = o
		}
	}
	for _, name := range sortedKeys(fields) {
		winner, err := topRank(fields[name], credRankOrder)
		if err != nil {
			return Recipe{}, fmt.Errorf("%w: %s (%s)", ErrAmbiguousCredentialContract, name, err)
		}
		field := ConnectionField{Name: name, Delivery: winner.Claim.Delivery, Target: winner.Claim.Target, Source: winner.Source}
		if winner.Claim.Required != nil {
			field.Required = *winner.Claim.Required
		}
		if winner.Claim.Secret != nil {
			field.Secret = *winner.Claim.Secret
		}
		if credRankOrder(winner.Rank) >= credRankOrder(RankR6) {
			field.Unverified = true
			field.Required = false
		}
		recipe.Credentials.Fields = append(recipe.Credentials.Fields, field)
		if o := credRankOrder(winner.Rank); o > worstCred {
			worstCred = o
		}
	}
	switch {
	case worstLaunch <= launchRankOrder(RankL0) && worstCred <= credRankOrder(RankR0):
		recipe.Trust = TrustT0
	case worstLaunch <= launchRankOrder(RankL3) && worstCred <= credRankOrder(RankR4):
		recipe.Trust = TrustT1
	default:
		recipe.Trust = TrustT2
	}
	return recipe, recipe.Validate()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// topRank picks the highest-ranked claim set and fails closed when it holds
// disagreeing claims — rank ties never resolve first-wins.
func topRank(entries []LedgerEntry, order func(Rank) int) (LedgerEntry, error) {
	best := entries[0]
	for _, entry := range entries[1:] {
		if order(entry.Rank) < order(best.Rank) {
			best = entry
		}
	}
	top := order(best.Rank)
	variants := map[string]bool{}
	for _, entry := range entries {
		if order(entry.Rank) == top {
			variants[claimKey(entry.Claim)] = true
		}
	}
	if len(variants) > 1 {
		sources := make([]string, 0)
		for _, entry := range entries {
			if order(entry.Rank) == top {
				sources = append(sources, entry.Source)
			}
		}
		slices.Sort(sources)
		return LedgerEntry{}, fmt.Errorf("%s vs %s", sources[0], sources[len(sources)-1])
	}
	return best, nil
}

func claimKey(claim Claim) string {
	body, _ := json.Marshal(claim)
	return string(body)
}

func applyLaunchClaim(launch *LaunchRecipe, attribute, rawValue string) error {
	value := json.RawMessage(rawValue)
	if !json.Valid(value) {
		return fmt.Errorf("%w: launch claim %s not JSON", ErrInvalid, attribute)
	}
	var err error
	switch attribute {
	case "artifact":
		err = json.Unmarshal(value, &launch.Artifact)
	case "endpoint":
		err = json.Unmarshal(value, &launch.Endpoint)
	case "entrypoint":
		err = json.Unmarshal(value, &launch.Entrypoint)
	default:
		return fmt.Errorf("%w: unknown launch attribute %s", ErrInvalid, attribute)
	}
	if err != nil {
		return fmt.Errorf("%w: launch claim %s", ErrInvalid, attribute)
	}
	return nil
}

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
