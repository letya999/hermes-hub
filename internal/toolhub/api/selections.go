package api

import (
	"fmt"
	"slices"
)

// DeriveConnectorSelections compiles one connector toolset's tool membership
// for a concrete binding. It is the single derivation shared by passport
// publish (apply) and the pending-selection materializer that fires when a
// binding activates — both must produce identical selections for identical
// (definition, binding, authored filter) inputs, because the profile's
// admission check replays published output.
//
// include/exclude carry the authored tools filter: nil means unfiltered.
// access "read" drops non-read effects. The toolset name is recorded on each
// selection so inventory can join membership back to the authored set.
func DeriveConnectorSelections(definition ToolDefinition, binding ToolBinding, toolsetName, access string, include, exclude []string, placement string) ([]CapabilitySelection, []string, error) {
	available := map[string]ToolSpec{}
	for _, tool := range definition.ProjectedToolSpecs(binding) {
		available[tool.Name] = tool
	}
	for _, member := range append(slices.Clone(include), exclude...) {
		if _, ok := available[member]; !ok {
			return nil, nil, fmt.Errorf("connector %q selects unknown tool %q", toolsetName, member)
		}
	}
	names := make([]string, 0, len(available))
	for member := range available {
		names = append(names, member)
	}
	slices.Sort(names)
	selections := make([]CapabilitySelection, 0, len(names))
	var core []string
	for _, member := range names {
		tool := available[member]
		if tool.Audience != "" && tool.Audience != AudienceModel || access == "read" && tool.Effect != ReadEffect ||
			include != nil && !slices.Contains(include, member) || slices.Contains(exclude, member) {
			continue
		}
		if tool.CapabilityID == "" || len(tool.Uses) == 0 && !RemoteExecutor(tool.Executor) {
			return nil, nil, fmt.Errorf("connector %q tool %q has no capability contract", toolsetName, member)
		}
		projected := ProjectedToolName(definition.DefinitionID, definition.Version, member)
		selections = append(selections, CapabilitySelection{CapabilityID: tool.CapabilityID, DefinitionID: definition.DefinitionID,
			DefinitionVersion: definition.Version, ImplementationDigest: DefinitionDigest(definition), ToolName: member, Name: projected, ConnectionID: binding.ConnectionID, Toolset: toolsetName})
		if placement == "core" {
			core = append(core, projected)
		}
	}
	return selections, core, nil
}
