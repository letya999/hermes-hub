// Typed v2 passport bodies live in internal/passportview — the leaf read
// path every consumer shares. These aliases keep the historical
// passport.<Type> names so compile, publish and callers stay put.
package passport

import "github.com/letya999/hermes-hub/internal/passportview"

// Names is the fixed six-passport order.
var Names = passportview.Names

// Current returns the active revision id.
var Current = passportview.Current

type (
	Header         = passportview.Header
	Metadata       = passportview.Metadata
	Stack          = passportview.Stack
	Identity       = passportview.Identity
	ServiceSpec    = passportview.ServiceSpec
	Config         = passportview.Config
	Runtime        = passportview.Runtime
	RuntimeContext = passportview.RuntimeContext
	Supervision    = passportview.Supervision
	Watcher        = passportview.Watcher
	Channel        = passportview.Channel
	Ref            = passportview.Ref
	Voice          = passportview.Voice
	Memory         = passportview.Memory
	Models         = passportview.Models
	Route          = passportview.Route
	ModelService   = passportview.ModelService
	Auth           = passportview.Auth
	AuthRef        = passportview.AuthRef
	Limit          = passportview.Limit
	ModelRef       = passportview.ModelRef
	Workspace      = passportview.Workspace
	Mount          = passportview.Mount
	Tools          = passportview.Tools
	Toolset        = passportview.Toolset
	Selector       = passportview.Selector
	Scope          = passportview.Scope
	Rule           = passportview.Rule
	Grants         = passportview.Grants
	StackDoc       = passportview.StackDoc
	ConfigDoc      = passportview.ConfigDoc
	ModelsDoc      = passportview.ModelsDoc
	WorkspaceDoc   = passportview.WorkspaceDoc
	ToolsDoc       = passportview.ToolsDoc
	GrantsDoc      = passportview.GrantsDoc
	Effective      = passportview.Effective
)

// Decode parses one published passport by name.
func Decode(name string, b []byte) (any, error) { return passportview.Decode(name, b) }

// LoadEffective reads the current revision as typed data.
func LoadEffective(spaceDir string) (Effective, error) { return passportview.LoadEffective(spaceDir) }
