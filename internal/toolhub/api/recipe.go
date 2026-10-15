package api

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// LaunchRecipe is the secret-free description of one MCP workload. It is a
// plan only: Resolver never executes its command, image, compose file or
// healthcheck.
type LaunchRecipe struct {
	Artifact   string           `json:"artifact,omitempty"`
	Version    string           `json:"version,omitempty"`
	Digest     string           `json:"digest,omitempty"`
	Transport  Transport        `json:"transport"`
	Endpoint   string           `json:"endpoint,omitempty"`
	Entrypoint []string         `json:"entrypoint,omitempty"`
	Args       []string         `json:"args,omitempty"`
	Network    []string         `json:"network,omitempty"`
	Mounts     []Mount          `json:"mounts,omitempty"`
	State      string           `json:"state,omitempty"`
	Health     HealthProbe      `json:"health,omitempty"`
	Sidecars   []string         `json:"sidecars,omitempty"`
	Evidence   []RecipeEvidence `json:"evidence,omitempty"`
}

type ConnectionField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Delivery    string `json:"delivery"`
	Target      string `json:"target,omitempty"`
	Alternative string `json:"alternative,omitempty"`
}

type ConnectionAlternative struct {
	Name   string            `json:"name"`
	Fields []ConnectionField `json:"fields,omitempty"`
	URL    string            `json:"url,omitempty"`
}

type ConnectionRecipe struct {
	Fields       []ConnectionField       `json:"fields,omitempty"`
	Alternatives []ConnectionAlternative `json:"alternatives,omitempty"`
}

func (r ConnectionRecipe) Validate() error {
	seen := map[string]bool{}
	for _, field := range r.Fields {
		if !CredentialPattern.MatchString(field.Name) || seen[field.Name] {
			return fmt.Errorf("%w: invalid connection field", ErrInvalid)
		}
		if field.Delivery != "env" && field.Delivery != "file" && field.Delivery != "json" && field.Delivery != "http_header" && field.Delivery != "oauth" {
			return fmt.Errorf("%w: %q", ErrUnsupportedCredentialContract, field.Delivery)
		}
		if field.Target != "" && strings.ContainsAny(field.Target, "\x00\r\n") {
			return fmt.Errorf("%w: invalid connection target", ErrInvalid)
		}
		seen[field.Name] = true
	}
	for _, alternative := range r.Alternatives {
		if alternative.Name == "" || len(alternative.Name) > 128 || strings.ContainsAny(alternative.Name, "\x00\r\n") || (alternative.URL != "" && (!strings.HasPrefix(strings.ToLower(alternative.URL), "https://") || HostOfURL(alternative.URL) == "")) {
			return fmt.Errorf("%w: invalid connection alternative", ErrInvalid)
		}
		if err := (ConnectionRecipe{Fields: alternative.Fields}).Validate(); err != nil {
			return err
		}
	}
	return nil
}

// CredentialInputs projects the recipe's declared contract into definition
// credentials. Delivery and Target ride along: a recipe-declared file
// credential must reach the workload as a mounted file, not silently shrink
// to an env var whose value is a path that does not exist in the container.
// An env/json target that just restates the variable name adds nothing —
// only targets carrying a real destination (mount path, header, host) stay.
func (r ConnectionRecipe) CredentialInputs() []CredentialInput {
	normalized := normalizeConnection(r)
	inputs := make([]CredentialInput, 0, len(normalized.Fields))
	for _, field := range normalized.Fields {
		target := field.Target
		if target == field.Name {
			target = ""
		}
		inputs = append(inputs, CredentialInput{Name: field.Name, Required: field.Required, Delivery: field.Delivery, Target: target})
	}
	return inputs
}

type RecipeEvidence struct {
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Digest string `json:"digest,omitempty"`
	Detail string `json:"detail"`
}

type RecipeLookup struct {
	Repository   string
	Subfolder    string
	CommitSHA    string
	Owner        string
	Name         string
	ImageHints   []string
	VersionHints []string
}

// RecipeCandidate is the bounded result returned by one external catalog.
// Repository and commit are mandatory for admission; a matching name alone is
// deliberately not enough.
type RecipeCandidate struct {
	Repository string           `json:"repository"`
	Subfolder  string           `json:"subfolder,omitempty"`
	CommitSHA  string           `json:"commit_sha,omitempty"`
	Launch     LaunchRecipe     `json:"launch"`
	Connection ConnectionRecipe `json:"connection,omitempty"`
	Evidence   []RecipeEvidence `json:"evidence,omitempty"`
}

type RecipeCatalog interface {
	Lookup(context.Context, RecipeLookup) ([]RecipeCandidate, error)
}

type RecipeResolution struct {
	Source     ArtifactSource   `json:"source"`
	Launch     LaunchRecipe     `json:"launch"`
	Connection ConnectionRecipe `json:"connection"`
	Evidence   []RecipeEvidence `json:"evidence,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
	State      string           `json:"state"`
}

func ScopeRecipeContext(files map[string][]byte, subfolder string) (map[string][]byte, error) {
	if subfolder == "" {
		return files, nil
	}
	if !validGitHubSubfolder(subfolder) {
		return nil, fmt.Errorf("%w: invalid source subfolder", ErrInvalid)
	}
	prefix := strings.TrimSuffix(subfolder, "/") + "/"
	scoped := map[string][]byte{}
	for name, content := range files {
		if relative, ok := strings.CutPrefix(name, prefix); ok && relative != "" && recipeContextPath(relative) {
			scoped[relative] = content
		}
	}
	if len(scoped) == 0 {
		return nil, fmt.Errorf("%w: source subfolder has no safe files", ErrInvalid)
	}
	return scoped, nil
}

func recipeContextPath(name string) bool {
	if ArtifactContextPath(name) {
		return true
	}
	if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00\r\n") {
		return false
	}
	base := strings.ToLower(path.Base(name))
	return base == ".mcp.json" || base == ".env.example"
}

func LookupRecipe(source ArtifactSource) (RecipeLookup, error) {
	u := strings.TrimPrefix(source.Repository, "https://github.com/")
	parts := strings.Split(u, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return RecipeLookup{}, fmt.Errorf("%w: source repository lookup", ErrInvalid)
	}
	return RecipeLookup{Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA, Owner: parts[0], Name: parts[1]}, nil
}

func CandidateMatches(candidate RecipeCandidate, source ArtifactSource) bool {
	if !sameRepository(candidate.Repository, source.Repository) || candidate.Subfolder != source.Subfolder {
		return false
	}
	return candidate.CommitSHA != "" && strings.EqualFold(candidate.CommitSHA, source.CommitSHA)
}

func sameRepository(left, right string) bool {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(left)), "/") == strings.TrimRight(strings.ToLower(strings.TrimSpace(right)), "/")
}
