package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

type RecipeResolver struct {
	Catalogs        []RecipeCatalog
	MaxContextBytes int64
}

func (r RecipeResolver) Resolve(ctx context.Context, source ArtifactSource, contextBytes []byte) (RecipeResolution, error) {
	if _, err := source.ArchiveURL(); err != nil {
		return RecipeResolution{}, err
	}
	Max := r.MaxContextBytes
	if Max == 0 {
		Max = 64 << 20
	}
	files, err := ReadRecipeContext(contextBytes, Max)
	if err != nil {
		return RecipeResolution{}, err
	}
	scopedFiles, err := ScopeRecipeContext(files, source.Subfolder)
	if err != nil {
		return RecipeResolution{}, err
	}
	lookup, err := LookupRecipe(source)
	if err != nil {
		return RecipeResolution{}, err
	}
	resolution := RecipeResolution{Source: source, State: "draft"}
	resolution.Evidence = contextEvidence(scopedFiles)

	// Catalogs are consulted first with source-derived lookup data only:
	// registry answers outrank repository files on both ladders, so a repo
	// hint must never steer which catalog record wins. Repository files are
	// parsed afterwards and contribute low-rank claims and warnings.
	candidates := LookupCatalogs(ctx, r.Catalogs, lookup)
	fetchedAt := time.Now().UTC().Format(time.RFC3339)
	for _, candidate := range candidates {
		resolution.Ledger = append(resolution.Ledger, candidateLedgerEntries(candidate, !CandidateMatches(candidate, source), fetchedAt)...)
	}

	localLaunch, localConnection, warnings, err := ResolveLocalRecipe(scopedFiles, source)
	if err != nil {
		return RecipeResolution{}, err
	}
	resolution.Launch = localLaunch
	resolution.Connection = localConnection
	resolution.Warnings = append(resolution.Warnings, warnings...)

	for _, candidate := range candidates {
		if !CandidateMatches(candidate, source) {
			resolution.Warnings = append(resolution.Warnings, "catalog candidate ignored: repository or commit did not match the pinned source")
			continue
		}
		resolution.Evidence = append(resolution.Evidence, candidate.Evidence...)
		resolution.Evidence = append(resolution.Evidence, candidate.Launch.Evidence...)
		if candidate.Launch.Artifact != "" || candidate.Launch.Endpoint != "" || len(candidate.Launch.Entrypoint) > 0 {
			if !SameLaunch(resolution.Launch, candidate.Launch) && HasLaunch(resolution.Launch) {
				resolution.State = "review"
				resolution.Warnings = append(resolution.Warnings, "matching catalogs returned conflicting launch recipes")
			} else if !HasLaunch(resolution.Launch) || resolution.State != "review" {
				resolution.Launch = candidate.Launch
			}
		}
		resolution.Connection = mergeConnection(resolution.Connection, connectionWithSource(candidate.Connection, "catalog"))
	}

	resolution.Connection = normalizeConnection(resolution.Connection)
	if explicit, ok := ctx.Value(ConnectionRecipeContextKey{}).(ConnectionRecipe); ok {
		resolution.Connection = mergeConnection(connectionWithSource(explicit, "install-recipe"), resolution.Connection)
	}
	if err := resolution.Connection.Validate(); err != nil {
		return RecipeResolution{}, err
	}
	// Re-run the recorded ledger to enrich the merged recipe: the winning
	// claims carry each field's ladder rank and token help link, and the
	// resolved trust tier summarises the worst winning rank for the form.
	// Locally parsed fields without ledger claims keep their Source tag. A
	// ledger-level disagreement degrades to a warning — the merge above and
	// the review gate already decided admissibility.
	enrichResolution(&resolution)
	if !HasLaunch(resolution.Launch) {
		resolution.Warnings = append(resolution.Warnings, "no complete launch recipe was proven; existing language fallback remains required")
	}
	// Ranked claims settle disagreements deterministically, so a disagreeing
	// warning never decides review; conflicts and unsafe directives still do.
	for _, warning := range resolution.Warnings {
		if strings.Contains(warning, "conflict") || strings.Contains(warning, "unsafe") {
			resolution.State = "review"
		}
	}
	if resolution.State != "review" && LaunchReady(resolution.Launch) {
		resolution.State = "ready"
	}
	slices.Sort(resolution.Warnings)
	return resolution, nil
}

// candidateLedgerEntries decomposes one fetched catalog response into ledger
// claims — one per launch attribute and credential field — so resolution can
// be replayed without repeating the fetch. Mismatched candidates stay in the
// ledger flagged ignored.
func candidateLedgerEntries(candidate RecipeCandidate, ignored bool, fetchedAt string) []LedgerEntry {
	body, _ := json.Marshal(candidate)
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	source := candidate.Source
	if source == "" {
		source = "catalog"
	}
	ref := candidate.Repository
	if candidate.CommitSHA != "" {
		ref += "@" + candidate.CommitSHA
	}
	quote := func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	var entries []LedgerEntry
	entry := func(claim Claim, rank Rank) {
		entries = append(entries, LedgerEntry{Claim: claim, Source: source, Ref: ref, Rank: rank, Digest: digest, FetchedAt: fetchedAt, Ignored: ignored})
	}
	if candidate.Launch.Artifact != "" {
		entry(Claim{Attribute: "artifact", Value: quote(candidate.Launch.Artifact)}, candidate.LaunchRank)
	}
	if candidate.Launch.Endpoint != "" {
		entry(Claim{Attribute: "endpoint", Value: quote(candidate.Launch.Endpoint)}, candidate.LaunchRank)
	}
	for _, field := range candidate.Connection.Fields {
		required, secret := field.Required, field.Secret
		entry(Claim{Field: field.Name, Required: &required, Secret: &secret, Delivery: field.Delivery, Target: field.Target, TokenURL: field.TokenURL}, candidate.CredentialRank)
	}
	return entries
}

// enrichResolution replays the recorded ledger and stamps the resolved trust
// tier plus per-field rank/token_url onto the merged connection recipe.
// Without usable claims the resolution keeps an empty tier — an empty ledger
// resolves to T0, which would misreport locally derived recipes as fully
// verified. Ledger disagreement surfaces as a warning, never a resolver error.
func enrichResolution(resolution *RecipeResolution) {
	usable := false
	for _, entry := range resolution.Ledger {
		if !entry.Ignored && (entry.Claim.Field != "" || entry.Claim.Attribute != "") {
			usable = true
			break
		}
	}
	if !usable {
		return
	}
	kind := SubjectCLI
	switch resolution.Launch.Transport {
	case ContainerMCP:
		kind = SubjectContainerMCP
	case RemoteMCP:
		kind = SubjectRemoteMCP
	}
	recipe, err := ResolveLedger(RecipeSubject{Kind: kind, Ref: resolution.Source.Repository}, resolution.Ledger)
	if err != nil {
		resolution.Warnings = append(resolution.Warnings, "ledger enrichment failed: "+err.Error())
		return
	}
	resolution.Trust = recipe.Trust
	provenance := map[string]ConnectionField{}
	for _, field := range recipe.Credentials.Fields {
		provenance[field.Name] = field
	}
	for i, field := range resolution.Connection.Fields {
		if proven, ok := provenance[field.Name]; ok {
			resolution.Connection.Fields[i].Rank = proven.Rank
			resolution.Connection.Fields[i].TokenURL = proven.TokenURL
		}
	}
}

func LookupCatalogs(ctx context.Context, catalogs []RecipeCatalog, lookup RecipeLookup) []RecipeCandidate {
	if len(catalogs) == 0 {
		return nil
	}
	results := make([][]RecipeCandidate, len(catalogs))
	var pending sync.WaitGroup
	for i, catalog := range catalogs {
		if catalog == nil {
			continue
		}
		pending.Add(1)
		go func(i int, catalog RecipeCatalog) {
			defer pending.Done()
			candidates, err := catalog.Lookup(ctx, lookup)
			if err == nil && len(candidates) <= 32 {
				results[i] = candidates
			}
		}(i, catalog)
	}
	pending.Wait()
	var flat []RecipeCandidate
	for _, candidates := range results {
		flat = append(flat, candidates...)
	}
	return flat
}

func ReadRecipeContext(data []byte, maxBytes int64) (map[string][]byte, error) {
	if maxBytes < 1 || maxBytes > 64<<20 || int64(len(data)) > maxBytes+1<<20 {
		return nil, fmt.Errorf("%w: resolver context is oversized", ErrInvalid)
	}
	reader := tar.NewReader(bytes.NewReader(data))
	files := map[string][]byte{}
	seen := map[string]bool{}
	var total int64
	for len(files) < 4096 {
		header, err := reader.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxBytes-total || !recipeContextPath(header.Name) || path.Clean(header.Name) != header.Name || seen[header.Name] {
			return nil, fmt.Errorf("%w: unsafe resolver context", ErrInvalid)
		}
		content, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(content)) != header.Size {
			return nil, fmt.Errorf("%w: invalid resolver context file", ErrInvalid)
		}
		files[header.Name] = content
		seen[header.Name] = true
		total += header.Size
	}
	return nil, fmt.Errorf("%w: resolver context has too many files", ErrInvalid)
}

func contextEvidence(files map[string][]byte) []RecipeEvidence {
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	evidence := make([]RecipeEvidence, 0, len(paths))
	for _, name := range paths {
		hash := sha256.Sum256(files[name])
		evidence = append(evidence, RecipeEvidence{Source: "github", Path: name, Digest: "sha256:" + hex.EncodeToString(hash[:]), Detail: "pinned repository file"})
	}
	return evidence
}

func ResolveLocalRecipe(files map[string][]byte, source ArtifactSource) (LaunchRecipe, ConnectionRecipe, []string, error) {
	var launch LaunchRecipe
	var connection ConnectionRecipe
	if data, ok := files["mcp-connection.json"]; ok {
		if !json.Valid(data) {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, ErrInvalid
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&connection); err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, err
		}
		if err := connection.Validate(); err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, err
		}
		connection = connectionWithSource(connection, "mcp-connection.json")
	}
	var warnings []string
	// decls keeps every tagged field declaration before the merge deduplicates
	// by name, so conflicting requiredness across sources can be reported.
	var decls []fieldDecl
	merge := func(recipe ConnectionRecipe, name string) {
		tagged := connectionWithSource(recipe, name)
		for _, field := range tagged.Fields {
			decls = append(decls, fieldDecl{name: field.Name, source: field.Source, required: field.Required})
		}
		connection = mergeConnection(connection, tagged)
	}
	data, name, err := findScopedFile(files, "server.json")
	if err != nil {
		return LaunchRecipe{}, ConnectionRecipe{}, nil, err
	}
	if data != nil {
		candidate, fields, ok, err := ParseServerManifest(data, name, source)
		if err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, err
		}
		if ok {
			launch = candidate
			merge(fields, name)
		}
	}
	if data, name := findFile(files, "manifest.json"); data != nil {
		candidate, fields, err := ParseMCPBManifest(data, name)
		if err != nil {
			return LaunchRecipe{}, ConnectionRecipe{}, nil, err
		}
		if HasLaunch(candidate) {
			launch = MergeLaunch(launch, candidate, &warnings)
		}
		merge(fields, name)
	}
	// The file loop must resolve identically for every call: map iteration is
	// unspecified, so names are visited in sorted order. Dockerfile and
	// Compose declarations are the same rank — when two of them disagree about
	// the launch recipe the resolution fails closed instead of first-wins.
	launchFromBuild := false
	mergeBuildLaunch := func(candidate LaunchRecipe, name string) error {
		if !HasLaunch(candidate) {
			return nil
		}
		if launchFromBuild && !SameLaunch(launch, candidate) {
			return fmt.Errorf("%w: %s disagrees with an earlier build declaration", ErrAmbiguousEntrypoint, name)
		}
		if !HasLaunch(launch) || launchFromBuild {
			launchFromBuild = true
		}
		launch = MergeLaunch(launch, candidate, &warnings)
		return nil
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		data := files[name]
		if testFixturePath(name) {
			continue
		}
		base := strings.ToLower(path.Base(name))
		switch {
		case strings.HasPrefix(base, "dockerfile"):
			candidate, fields, fileWarnings, err := parseDockerfile(data, name)
			if err != nil {
				return LaunchRecipe{}, ConnectionRecipe{}, nil, err
			}
			if err := mergeBuildLaunch(candidate, name); err != nil {
				return LaunchRecipe{}, ConnectionRecipe{}, nil, err
			}
			merge(fields, name)
			warnings = append(warnings, fileWarnings...)
		case base == "compose.yaml" || base == "compose.yml" || strings.HasPrefix(base, "docker-compose."):
			candidate, fields, fileWarnings, err := parseCompose(data, name)
			if err != nil {
				// Ambiguity in reviewed metadata fails closed with a stable code
				// instead of degrading to a warning that later reads as an
				// unexplained "no launch recipe".
				if errors.Is(err, ErrAmbiguousEntrypoint) {
					return LaunchRecipe{}, ConnectionRecipe{}, nil, err
				}
				warnings = append(warnings, "Compose recipe rejected: "+err.Error())
				continue
			}
			if err := mergeBuildLaunch(candidate, name); err != nil {
				return LaunchRecipe{}, ConnectionRecipe{}, nil, err
			}
			merge(fields, name)
			warnings = append(warnings, fileWarnings...)
		case strings.HasPrefix(base, "readme"):
			// Documentation mentions prove a variable exists, not that it is
			// required — merge them for coverage but keep them out of the
			// conflict accounting.
			connection = mergeConnection(connection, connectionWithSource(ParseDocumentationCredentials(data, name), name))
		case base == ".mcp.json":
			candidate, fields, err := ParseMCPJSON(data, name)
			if err != nil {
				return LaunchRecipe{}, ConnectionRecipe{}, nil, err
			}
			launch = MergeLaunch(launch, candidate, &warnings)
			merge(fields, name)
		case base == ".env.example":
			merge(ParseExampleEnv(data, name), name)
		}
	}
	warnings = append(warnings, connectionSourceConflicts(decls)...)
	if source.Subfolder != "" {
		warnings = append(warnings, "subfolder is pinned; generated build fallback must preserve that working directory")
	}
	return launch, normalizeConnection(connection), warnings, nil
}

// fieldDecl is one tagged field declaration before dedup: name, the file it
// came from, and whether that file marked it required.
type fieldDecl struct {
	name     string
	source   string
	required bool
}

// connectionSourceConflicts names every field declared with different
// requiredness across metadata files, listing which source said what so the
// caller can surface the disagreement instead of guessing.
func connectionSourceConflicts(decls []fieldDecl) []string {
	byName := map[string]map[string]bool{}
	for _, decl := range decls {
		kind := "optional"
		if decl.required {
			kind = "required"
		}
		if byName[decl.name] == nil {
			byName[decl.name] = map[string]bool{}
		}
		byName[decl.name][decl.source+": "+kind] = true
	}
	var warnings []string
	for name, variants := range byName {
		if len(variants) < 2 {
			continue
		}
		parts := make([]string, 0, len(variants))
		for variant := range variants {
			parts = append(parts, variant)
		}
		slices.Sort(parts)
		warnings = append(warnings, "credential sources disagree about "+name+" ("+strings.Join(parts, ", ")+")")
	}
	slices.Sort(warnings)
	return warnings
}
