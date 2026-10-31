package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	ToolContractPreflight      = "preflight-list"
	ToolContractReviewManifest = "review-manifest"
)

// ConfirmedToolContract is the only input that can make an imported packet
// eligible for trusted registration. ArtifactImportConfig.Tools is a review
// preview and is never sufficient on its own.
type ConfirmedToolContract struct {
	Source string     `json:"source"`
	Tools  []ToolSpec `json:"tools"`
}

func ConfirmedToolContractDigest(contract ConfirmedToolContract) (string, error) {
	if err := validateConfirmedToolContract(contract); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Source string     `json:"source"`
		Tools  []ToolSpec `json:"tools"`
	}{Source: contract.Source, Tools: canonicalToolSpecs(contract.Tools)})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func validateConfirmedToolContract(contract ConfirmedToolContract) error {
	if contract.Source != ToolContractPreflight && contract.Source != ToolContractReviewManifest {
		return fmt.Errorf("%w: tool contract must come from a real MCP tools/list or a review manifest checked against one", ErrUnauthorized)
	}
	if len(contract.Tools) == 0 || len(contract.Tools) > 256 {
		return fmt.Errorf("%w: confirmed tool contract must be bounded and non-empty", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, tool := range contract.Tools {
		if !McpToolNamePattern.MatchString(tool.Name) || (tool.Effect != ReadEffect && tool.Effect != WriteEffect) || seen[tool.Name] {
			return fmt.Errorf("%w: invalid confirmed tool %q", ErrInvalid, tool.Name)
		}
		seen[tool.Name] = true
	}
	return nil
}

func canonicalToolSpecs(tools []ToolSpec) []ToolSpec {
	out := append([]ToolSpec(nil), tools...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sameToolIdentities(claimed, confirmed []ToolSpec) bool {
	if len(claimed) != len(confirmed) {
		return false
	}
	effects := map[string]Effect{}
	for _, tool := range claimed {
		effects[tool.Name] = tool.Effect
	}
	for _, tool := range confirmed {
		if effects[tool.Name] != tool.Effect {
			return false
		}
		delete(effects, tool.Name)
	}
	return len(effects) == 0
}

// ApplyPreflightToolList replaces claimed ArtifactImportConfig tools with a
// real MCP tools/list, recomputes the review digest, and stamps the preflight
// contract. It does not rebuild the OCI archive.
func ApplyPreflightToolList(imported *ImportedArtifact, tools []ToolSpec) (ConfirmedToolContract, error) {
	if imported == nil {
		return ConfirmedToolContract{}, fmt.Errorf("%w: imported artifact required", ErrInvalid)
	}
	AlignImportedEntrypoint(imported)
	tools = applyCuratedReadTools(imported.Definition.Source, tools)
	contract := ConfirmedToolContract{Source: ToolContractPreflight, Tools: append([]ToolSpec(nil), tools...)}
	if err := validateConfirmedToolContract(contract); err != nil {
		return ConfirmedToolContract{}, err
	}
	imported.Definition.Tools = append([]ToolSpec(nil), contract.Tools...)
	digest, err := ReviewDigestForImported(*imported)
	if err != nil {
		return ConfirmedToolContract{}, err
	}
	imported.Definition.Source.ReviewDigest = digest
	if err := AttachConfirmedToolContract(imported, contract); err != nil {
		return ConfirmedToolContract{}, err
	}
	return contract, nil
}

// applyCuratedReadTools upgrades a probed tool to read when the prepared
// catalog curates it as read (e.g. POST search/query endpoints the upstream
// server never annotates readOnlyHint). The list is owner-authored, not
// provider text; unlisted tools keep the probe's fail-closed verdict.
func applyCuratedReadTools(source DefinitionSource, tools []ToolSpec) []ToolSpec {
	repository := strings.TrimSpace(source.Repository)
	if repository == "" {
		return tools
	}
	entry, ok, err := PreparedForRepository(repository, strings.TrimSpace(source.Subfolder))
	if err != nil || !ok || len(entry.ReadTools) == 0 {
		return tools
	}
	curated := make(map[string]bool, len(entry.ReadTools))
	for _, name := range entry.ReadTools {
		curated[name] = true
	}
	out := append([]ToolSpec(nil), tools...)
	for i := range out {
		if curated[out[i].Name] {
			out[i].Effect = ReadEffect
		}
	}
	return out
}

// AttachConfirmedToolContract stamps an imported review packet with a tool
// contract that already matched a real MCP tools/list (or a review manifest
// checked against one). Claimed import tools must match names and effects.
func AttachConfirmedToolContract(imported *ImportedArtifact, contract ConfirmedToolContract) error {
	if imported == nil {
		return fmt.Errorf("%w: imported artifact required", ErrInvalid)
	}
	if err := validateConfirmedToolContract(contract); err != nil {
		return err
	}
	if !sameToolIdentities(imported.Definition.Tools, contract.Tools) {
		return fmt.Errorf("%w: confirmed tool contract does not match the review packet", ErrStale)
	}
	digest, err := ConfirmedToolContractDigest(contract)
	if err != nil {
		return err
	}
	imported.Definition.Source.ToolContractSource = contract.Source
	imported.Definition.Source.ToolContractDigest = digest
	return nil
}

func VerifyDefinitionToolContract(definition ToolDefinition) error {
	if definition.Source.ToolContractDigest == "" || definition.Source.ToolContractSource == "" {
		return fmt.Errorf("%w: confirmed tool contract required", ErrUnauthorized)
	}
	expected, err := ConfirmedToolContractDigest(ConfirmedToolContract{Source: definition.Source.ToolContractSource, Tools: definition.Tools})
	if err != nil {
		return err
	}
	if expected != definition.Source.ToolContractDigest {
		return fmt.Errorf("%w: tool contract digest drift", ErrStale)
	}
	return nil
}
