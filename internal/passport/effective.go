package passport

// Effective decodes this revision's six passports into the same typed view
// LoadEffective returns — the bridge apply uses before anything is published.
func (r Revision) Effective() (Effective, error) {
	var eff Effective
	for _, name := range Names {
		doc, err := Decode(name, r.Docs[name])
		if err != nil {
			return eff, err
		}
		switch d := doc.(type) {
		case *StackDoc:
			eff.Header = d.Header
			eff.Stack = d.Stack
		case *ConfigDoc:
			eff.Config = d.Config
		case *ModelsDoc:
			eff.Models = d.Models
		case *WorkspaceDoc:
			eff.Workspace = d.Workspace
		case *ToolsDoc:
			eff.Tools = d.Tools
		case *GrantsDoc:
			eff.Grants = d.Grants
		}
	}
	return eff, nil
}
