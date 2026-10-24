package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// hermesAgentRepository is the upstream Hermes tree whose optional-mcps
// directory is the only hermes: import target.
const hermesAgentRepository = "https://github.com/nousresearch/hermes-agent"

var importNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$`)

// ParseImportRef turns a deterministic T1 import ref into a recipe draft.
// Supported schemes: mcp-registry:<name>@<version>, docker:mcp/<name>@<digest>,
// toolhive:<name>, mcpb:<owner>/<repo>@<tag>, hermes:<name>@<commit> and
// aqua:<pkg>@<version>. The draft carries no fetched claims — the ledger
// records exactly what the ref asserted, so the same ref always yields the
// same review digest.
func ParseImportRef(ref string) (RecipeResolution, error) {
	scheme, rest, found := strings.Cut(ref, ":")
	if !found || rest == "" {
		return RecipeResolution{}, fmt.Errorf("%w: import ref needs a scheme", ErrInvalid)
	}
	var source ArtifactSource
	var launch LaunchRecipe
	switch scheme {
	case "mcp-registry":
		name, version, ok := splitImportRef(rest)
		if !ok || !importNamePattern.MatchString(name) {
			return RecipeResolution{}, fmt.Errorf("%w: mcp-registry ref is <name>@<version>", ErrInvalid)
		}
		source = ArtifactSource{PackageRegistry: "mcp-registry", PackageName: name, PackageVersion: version}
	case "docker":
		name, digest, ok := splitImportRef(rest)
		if !ok || !strings.HasPrefix(name, "mcp/") || !importNamePattern.MatchString(strings.TrimPrefix(name, "mcp/")) || !DigestPattern.MatchString(digest) {
			return RecipeResolution{}, fmt.Errorf("%w: docker ref is mcp/<name>@sha256:<digest>", ErrInvalid)
		}
		launch = LaunchRecipe{Transport: ContainerMCP, Artifact: name, Digest: digest}
	case "toolhive":
		if !importNamePattern.MatchString(rest) {
			return RecipeResolution{}, fmt.Errorf("%w: toolhive ref is <name>", ErrInvalid)
		}
		source = ArtifactSource{PackageRegistry: "toolhive", PackageName: rest}
	case "mcpb":
		repo, tag, ok := splitImportRef(rest)
		if !ok || strings.Count(repo, "/") != 1 || !importNamePattern.MatchString(repo) {
			return RecipeResolution{}, fmt.Errorf("%w: mcpb ref is <owner>/<repo>@<tag>", ErrInvalid)
		}
		source = ArtifactSource{Repository: "https://github.com/" + repo, Tag: tag}
	case "hermes":
		name, commit, ok := splitImportRef(rest)
		if !ok || !importNamePattern.MatchString(name) || !gitSHAPattern.MatchString(commit) {
			return RecipeResolution{}, fmt.Errorf("%w: hermes ref is <name>@<commit>", ErrInvalid)
		}
		source = ArtifactSource{Repository: hermesAgentRepository, CommitSHA: commit, Subfolder: "optional-mcps/" + name}
	case "aqua":
		pkg, version, ok := splitImportRef(rest)
		if !ok || !importNamePattern.MatchString(pkg) {
			return RecipeResolution{}, fmt.Errorf("%w: aqua ref is <pkg>@<version>", ErrInvalid)
		}
		source = ArtifactSource{PackageRegistry: "aqua", PackageName: pkg, PackageVersion: version}
	default:
		return RecipeResolution{}, fmt.Errorf("%w: unknown import scheme %q", ErrInvalid, scheme)
	}
	value, _ := json.Marshal(rest)
	return RecipeResolution{
		Source:   source,
		Launch:   launch,
		Trust:    TrustT1,
		State:    "draft",
		Ledger:   []LedgerEntry{{Claim: Claim{Attribute: "ref", Value: string(value)}, Source: scheme, Ref: ref, Rank: RankR1}},
		Warnings: []string{"imported ref requires operator confirmation before install"},
	}, nil
}

func splitImportRef(rest string) (string, string, bool) {
	name, version, ok := strings.Cut(rest, "@")
	return name, version, ok && name != "" && version != "" && !strings.Contains(version, "@")
}
