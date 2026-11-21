package agenttools

// Catalog seam (CHG-0337): the reviewed tool catalog definition lives in
// internal/catalog — it is projected by the host-side passport compile, so it
// can never be service-internal. The executor aliases it so moved code keeps
// compiling unchanged.
import "github.com/letya999/hermes-hub/internal/catalog"

var HubToolsDefinition = catalog.HubToolsDefinition

const HubToolsDefinitionID = catalog.HubToolsDefinitionID
