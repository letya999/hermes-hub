package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Ownership label keys stamped on every Docker resource the hub creates.
// Finalizers and the reconciler find a connection's footprint by these
// labels; objects without them are foreign and never touched.
const (
	LabelOwner      = "hermes-hub.owner"
	LabelConnection = "hermes-hub.connection"
	LabelBinding    = "hermes-hub.binding"
	LabelOperation  = "hermes-hub.operation"
	LabelRecipe     = "hermes-hub.recipe"
)

// OwnershipLabels carries the identity of the store records a spawned
// resource belongs to. Empty values are dropped — a partial set still
// scopes the resource to the owner and connection.
type OwnershipLabels struct {
	Owner      string `json:"owner,omitempty"`
	Connection string `json:"connection,omitempty"`
	Binding    string `json:"binding,omitempty"`
	Operation  string `json:"operation,omitempty"`
	Recipe     string `json:"recipe,omitempty"`
}

// Map returns the label key/value pairs with empty values dropped.
func (o OwnershipLabels) Map() map[string]string {
	labels := map[string]string{}
	for k, v := range map[string]string{
		LabelOwner:      o.Owner,
		LabelConnection: o.Connection,
		LabelBinding:    o.Binding,
		LabelOperation:  o.Operation,
		LabelRecipe:     o.Recipe,
	} {
		if v != "" {
			labels[k] = v
		}
	}
	return labels
}

// PlanOwnership derives the label set stamped on resources spawned for one
// effective binding: the principal owns them, the connection and binding
// scope removal, the recipe digest marks drift.
func PlanOwnership(effective EffectiveBinding) OwnershipLabels {
	ownership := OwnershipLabels{
		Owner:     effective.Binding.PrincipalID,
		Binding:   effective.Binding.ToolBindingID,
		Recipe:    effective.Definition.Source.RecipeDigest,
	}
	if effective.Connection != nil {
		ownership.Connection = effective.Connection.ConnectionID
	}
	return ownership
}

// WorkspaceOwnershipFile is the marker file a materialized workload
// workspace carries so the workspace finalizer can prove ownership.
const WorkspaceOwnershipFile = ".hermes-owner"

// ReadWorkspaceOwnership loads the marker file from a workspace directory.
func ReadWorkspaceOwnership(dir string) (OwnershipLabels, error) {
	body, err := os.ReadFile(filepath.Join(dir, WorkspaceOwnershipFile))
	if err != nil {
		return OwnershipLabels{}, err
	}
	var labels OwnershipLabels
	if err := json.Unmarshal(body, &labels); err != nil {
		return OwnershipLabels{}, fmt.Errorf("%w: workspace ownership marker", ErrInvalid)
	}
	return labels, nil
}

// WriteWorkspaceOwnership stamps the marker file into a workspace directory.
func WriteWorkspaceOwnership(dir string, labels OwnershipLabels) error {
	body, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, WorkspaceOwnershipFile), body, 0600)
}

// Args returns sorted `--label k=v` argv so spawn paths stay deterministic.
func (o OwnershipLabels) Args() []string {
	labels := o.Map()
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, "--label", k+"="+labels[k])
	}
	return args
}
