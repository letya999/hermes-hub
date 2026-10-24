// Package systemguide contains the reviewed, public Hermes Hub reference.
package systemguide

import _ "embed"

// Skill is shared by the native skill installation and the ToolHub read surface.
//
//go:embed SKILL.md
var Skill string
