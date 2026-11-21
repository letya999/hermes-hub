// Typed v2 passport bodies live in contracts/spawn/v1 — the leaf read
// path every consumer shares. These aliases keep the historical
// passport.<Type> names so compile, publish and callers stay put.
package passport

import spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"

// Names is the fixed six-passport order.
var Names = spawnv1.Names

type (
	Header         = spawnv1.Header
	Metadata       = spawnv1.Metadata
	Stack          = spawnv1.Stack
	Identity       = spawnv1.Identity
	ServiceSpec    = spawnv1.ServiceSpec
	Config         = spawnv1.Config
	Runtime        = spawnv1.RuntimeConfig
	RuntimeContext = spawnv1.RuntimeContext
	Supervision    = spawnv1.Supervision
	Watcher        = spawnv1.Watcher
	Channel        = spawnv1.Channel
	Ref            = spawnv1.Ref
	Voice          = spawnv1.Voice
	Memory         = spawnv1.Memory
	Models         = spawnv1.Models
	Route          = spawnv1.Route
	ModelService   = spawnv1.ModelService
	Auth           = spawnv1.Auth
	AuthRef        = spawnv1.AuthRef
	Limit          = spawnv1.Limit
	ModelRef       = spawnv1.ModelRef
	Workspace      = spawnv1.Workspace
	Mount          = spawnv1.Mount
	Tools          = spawnv1.Tools
	Toolset        = spawnv1.Toolset
	Selector       = spawnv1.Selector
	Scope          = spawnv1.Scope
	Rule           = spawnv1.Rule
	Grants         = spawnv1.Grants
	StackDoc       = spawnv1.StackDoc
	ConfigDoc      = spawnv1.ConfigDoc
	ModelsDoc      = spawnv1.ModelsDoc
	WorkspaceDoc   = spawnv1.WorkspaceDoc
	ToolsDoc       = spawnv1.ToolsDoc
	GrantsDoc      = spawnv1.GrantsDoc
	Effective      = spawnv1.Effective
)

// Decode parses one published passport by name.
func Decode(name string, b []byte) (any, error) { return spawnv1.Decode(name, b) }
