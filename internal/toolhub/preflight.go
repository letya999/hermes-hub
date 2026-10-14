package toolhub

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// PreflightImportedArtifact loads the quarantine image into local Docker,
// runs tools/list with the MCP runtime profile (not the BuildKit bootstrap
// profile), and restamps the review packet with that confirmed contract.
func PreflightImportedArtifact(ctx context.Context, imported ImportedArtifact, artifactsDir string) (ImportedArtifact, ConfirmedToolContract, error) {
	if _, err := storedArtifactLoader(ctx, artifactsDir, imported.Artifact.ArchiveDigest, imported.Definition.Source.Image, 8<<30); err != nil {
		return ImportedArtifact{}, ConfirmedToolContract{}, err
	}
	if err := preparedCredentialGate(ArtifactSource{Repository: imported.Definition.Source.Repository, CommitSHA: imported.Definition.Source.CommitSHA, Subfolder: imported.Definition.Source.Subfolder}); err != nil {
		return imported, ConfirmedToolContract{}, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if prepared, ok, _ := preparedForSource(ArtifactSource{Repository: imported.Definition.Source.Repository, CommitSHA: imported.Definition.Source.CommitSHA, Subfolder: imported.Definition.Source.Subfolder}); ok && prepared.PreflightNetwork {
		listCtx = withProbeOptions(listCtx, probeOptions{allowNetwork: true})
	}
	list := localMCPToolList
	tools, err := list(listCtx, imported)
	if err != nil {
		tools, imported, list, err = networkTransportFallback(listCtx, imported, err, list)
	}
	if err != nil {
		promoted, retry, retryErr := retryListWithPromotedCredentials(listCtx, imported, err, list)
		if retryErr != nil {
			combined := fmt.Errorf("%v [credential-promotion retry: %v]", err, retryErr)
			base := imported
			if promoted.Definition.DefinitionID != "" {
				base = promoted
			}
			if gated, gate, ok := credentialGateFrom(base, combined); ok {
				return gated, ConfirmedToolContract{}, gate
			}
			return ImportedArtifact{}, ConfirmedToolContract{}, combined
		}
		imported, tools = promoted, retry
	}
	if len(imported.Definition.Credentials) == 0 {
		imported.Definition.Credentials = discoverMCPHelpCredentials(listCtx, imported)
	}
	return restampFromMCPList(listCtx, imported, func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return tools, nil })
}

// PreflightPublishedArtifact pulls only the reviewed immutable image and runs
// the same isolated MCP initialize/tools-list probe used for local artifacts.
func PreflightPublishedArtifact(ctx context.Context, imported ImportedArtifact) (ImportedArtifact, ConfirmedToolContract, error) {
	if imported.Definition.Source.ArchiveDigest != "" || imported.Definition.Source.Repository == "" || !digestPattern.MatchString(imported.Definition.Source.Digest) {
		return ImportedArtifact{}, ConfirmedToolContract{}, fmt.Errorf("%w: published artifact evidence required", ErrInvalid)
	}
	if err := publishedImagePull(ctx, imported.Definition.Source.Image, imported.Definition.Source.Digest); err != nil {
		return ImportedArtifact{}, ConfirmedToolContract{}, err
	}
	if err := preparedCredentialGate(ArtifactSource{Repository: imported.Definition.Source.Repository, CommitSHA: imported.Definition.Source.CommitSHA, Subfolder: imported.Definition.Source.Subfolder}); err != nil {
		return imported, ConfirmedToolContract{}, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	list := publishedMCPToolList
	tools, err := list(listCtx, imported)
	if err != nil {
		tools, imported, list, err = networkTransportFallback(listCtx, imported, err, list)
	}
	if err != nil {
		promoted, retry, retryErr := retryListWithPromotedCredentials(listCtx, imported, err, list)
		if retryErr != nil {
			combined := fmt.Errorf("%v [credential-promotion retry: %v]", err, retryErr)
			base := imported
			if promoted.Definition.DefinitionID != "" {
				base = promoted
			}
			if gated, gate, ok := credentialGateFrom(base, combined); ok {
				return gated, ConfirmedToolContract{}, gate
			}
			return ImportedArtifact{}, ConfirmedToolContract{}, combined
		}
		imported, tools = promoted, retry
	}
	return restampFromMCPList(listCtx, imported, func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return tools, nil })
}

var authErrorEnvVar = regexp.MustCompile(`\b(?:set|missing|provide|export)\s+(?:the\s+)?(?:environment\s+(?:variable|variable\s+name)\s+)?([A-Z][A-Z0-9_]{2,63})\b|\b([A-Z][A-Z0-9_]{2,63})\s+(?:is\s+required|environment\s+variable\s+is\s+required|not\s+set|must\s+be\s+set)`)
var authFailureText = regexp.MustCompile(`(?i)authentication required|must be provided|must be set|is required|not set|missing credential`)
var envNameInText = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,63}\b`)
var bothCredentialPair = regexp.MustCompile(`both\s+([A-Z][A-Z0-9_]{2,63})\s+and\s+([A-Z][A-Z0-9_]{2,63})`)

// CredentialGate is an isolated tools/list that never started because the
// server named the secrets it needs first. It is not an empty catalog and not
// a confirmed tool contract: onboarding collects those secrets on the
// protected form, then lists tools with the submitted values.
type CredentialGate struct {
	Names  []string
	Groups [][]string
	Detail string
}

func (e *CredentialGate) Error() string {
	if e == nil {
		return "MCP refused tools/list until credentials exist"
	}
	return "MCP refused tools/list until credentials exist: " + strings.Join(e.Names, ", ")
}

func (e *CredentialGate) Unwrap() error { return ErrIsolation }

// authErrorCredentialNames extracts secret-shaped env names a failed server
// printed in its auth error. Only TOKEN/SECRET/KEY-style names are promoted:
// plain config variables mentioned in usage text are not credentials.
func authErrorCredentialNames(err error) []string {
	if err == nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	text := err.Error()
	for _, match := range authErrorEnvVar.FindAllStringSubmatch(text, 8) {
		rememberAuthCredential(&names, seen, match[1])
		rememberAuthCredential(&names, seen, match[2])
	}
	// Servers such as slack-mcp-server exit before MCP with a sentence like
	// "Either FOO_TOKEN, BAR_TOKEN, or both BAZ_TOKEN and QUX_TOKEN must be
	// provided". The names are not in the "set FOO" shape, but they are still
	// the credential gate. Only secret-shaped names count, and only when the
	// text is an auth failure, so usage prose does not become a form.
	if authFailureText.MatchString(text) {
		for _, name := range envNameInText.FindAllString(text, 32) {
			if len(names) >= 8 {
				break
			}
			rememberAuthCredential(&names, seen, name)
		}
	}
	return names
}

func rememberAuthCredential(names *[]string, seen map[string]bool, name string) {
	if name == "" || seen[name] || !secretName(name) || !credentialPattern.MatchString(name) {
		return
	}
	seen[name] = true
	*names = append(*names, name)
}

// credentialAlternativeGroups splits an either/or auth sentence into OR
// groups. "both A and B" is one group. A single name, or a list that is not
// phrased as alternatives, stays nil so every name is independently required.
func credentialAlternativeGroups(text string, names []string) [][]string {
	if len(names) < 2 {
		return nil
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "either") && !strings.Contains(lower, " or ") {
		return nil
	}
	known := map[string]bool{}
	for _, name := range names {
		known[name] = true
	}
	paired := map[string]bool{}
	seen := map[string]bool{}
	var groups [][]string
	for _, match := range bothCredentialPair.FindAllStringSubmatch(text, 8) {
		key := match[1] + "\x00" + match[2]
		if seen[key] || !known[match[1]] || !known[match[2]] {
			continue
		}
		seen[key] = true
		groups = append(groups, []string{match[1], match[2]})
		paired[match[1]], paired[match[2]] = true, true
	}
	for _, name := range names {
		if !paired[name] {
			groups = append(groups, []string{name})
		}
	}
	if len(groups) < 2 {
		return nil
	}
	return groups
}

// errEmptyToolList marks a probe where the server answered tools/list with a
// valid response carrying zero tools — distinct from a process that died or
// never answered, so a reviewer may treat answered-empty as a credential or
// configuration gate.
var errEmptyToolList = errors.New("MCP tools/list answered with zero tools")

func credentialGateFrom(imported ImportedArtifact, err error) (ImportedArtifact, *CredentialGate, bool) {
	if err == nil {
		return ImportedArtifact{}, nil, false
	}
	names := authErrorCredentialNames(err)
	emptyTools := false
	if len(names) == 0 {
		names = emptyToolsCredentialNames(imported, err)
		emptyTools = len(names) > 0
	}
	if len(names) == 0 {
		return ImportedArtifact{}, nil, false
	}
	for _, name := range names {
		if !credentialNameDeclared(imported.Definition.Credentials, name) {
			imported.Definition.Credentials = append(imported.Definition.Credentials, CredentialInput{Name: name, Required: true})
		}
	}
	named := map[string]bool{}
	for _, name := range names {
		named[name] = true
	}
	for index := range imported.Definition.Credentials {
		input := &imported.Definition.Credentials[index]
		if named[input.Name] && secretName(input.Name) {
			input.Required = true
		}
	}
	// Placeholder import tools are not a tools/list. Drop them so a caller that
	// only looks at the packet cannot register a contract the server never spoke.
	imported.Definition.Tools = nil
	imported.Definition.Source.ToolContractDigest = ""
	imported.Definition.Source.ToolContractSource = ""
	// Either/or alternatives stay on the definition so runtime admission asks
	// for one complete group instead of every required input at once. Members
	// that did not end up required (non-secret names) cannot satisfy a group.
	required := map[string]bool{}
	for _, input := range imported.Definition.Credentials {
		if input.Required {
			required[input.Name] = true
		}
	}
	var groups [][]string
	for _, group := range credentialAlternativeGroups(err.Error(), names) {
		var members []string
		for _, name := range group {
			if required[name] {
				members = append(members, name)
			}
		}
		if len(members) > 0 {
			groups = append(groups, members)
		}
	}
	if len(groups) < 2 {
		groups = nil
	}
	if groups == nil && emptyTools {
		groups = emptyToolsCredentialGroups(imported.Definition.Credentials)
	}
	imported.Definition.CredentialGroups = groups
	return imported, &CredentialGate{
		Names:  names,
		Groups: groups,
		Detail: publicPrepareError(err),
	}, true
}

// emptyToolsCredentialNames promotes a "tools/list answered with zero tools"
// probe failure into a credential gate when the definition already declares
// required inputs. Servers such as mcp-atlassian register no tools until
// their connection config exists; without this path the review would fail
// outright instead of asking for the declared credentials. A process that
// died without answering (no errEmptyToolList) or a definition with no
// required inputs still fails the review.
func emptyToolsCredentialNames(imported ImportedArtifact, err error) []string {
	if err == nil || !errors.Is(err, errEmptyToolList) {
		return nil
	}
	var names []string
	for _, input := range imported.Definition.Credentials {
		if input.Required {
			names = append(names, input.Name)
		}
	}
	slices.Sort(names)
	return names
}

// emptyToolsCredentialGroups groups required inputs by their PREFIX_
// namespace so a server that accepts either of two product configurations
// (JIRA_* or CONFLUENCE_*) asks for one complete set instead of every input
// at once. A prefix becomes an alternative only when it covers at least two
// inputs and one of them is a secret; otherwise inputs stay independently
// required.
func emptyToolsCredentialGroups(inputs []CredentialInput) [][]string {
	var order []string
	buckets := map[string][]string{}
	for _, input := range inputs {
		if !input.Required {
			continue
		}
		prefix, _, _ := strings.Cut(input.Name, "_")
		if prefix == "" {
			continue
		}
		if _, ok := buckets[prefix]; !ok {
			order = append(order, prefix)
		}
		buckets[prefix] = append(buckets[prefix], input.Name)
	}
	var groups [][]string
	for _, prefix := range order {
		members := buckets[prefix]
		if len(members) < 2 || !slices.ContainsFunc(members, secretName) {
			continue
		}
		groups = append(groups, members)
	}
	if len(groups) < 2 {
		return nil
	}
	return groups
}

func credentialNameDeclared(inputs []CredentialInput, name string) bool {
	for _, input := range inputs {
		if input.Name == name {
			return true
		}
	}
	return false
}

// admitWithSubmittedCredentials runs tools/list with the owner's submitted
// secrets and, only for this probe, with network egress. A confirmed tool
// list is the only result that may be registered. The placeholder value
// "preflight" is not used here.
func admitWithSubmittedCredentials(ctx context.Context, artifactsDir string, definition ToolDefinition, secrets map[string]string) (ToolDefinition, error) {
	if definition.Source.ArchiveDigest != "" {
		if _, err := storedArtifactLoader(ctx, artifactsDir, definition.Source.ArchiveDigest, definition.Source.Image, 8<<30); err != nil {
			return ToolDefinition{}, err
		}
	}
	imported := ImportedArtifact{Definition: definition}
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	probeCtx = withProbeOptions(probeCtx, probeOptions{values: secrets, allowNetwork: true})
	list := localMCPToolList
	if imported.Definition.Source.NetworkTransport != "" {
		list = networkMCPToolList
	}
	tools, err := list(probeCtx, imported)
	if err != nil {
		return ToolDefinition{}, err
	}
	if _, err := ApplyPreflightToolList(&imported, tools); err != nil {
		return ToolDefinition{}, err
	}
	return imported.Definition, nil
}
