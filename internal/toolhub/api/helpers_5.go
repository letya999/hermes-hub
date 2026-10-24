package api

import (
	"path/filepath"
	"strings"
)

// CLIApproval pins one admitted bounded-cli plan shape.
type CLIApproval struct {
	DefinitionID      string          `json:"definition_id"`
	DefinitionVersion string          `json:"definition_version"`
	Command           string          `json:"command"`
	Image             string          `json:"image,omitempty"`
	Digest            string          `json:"digest,omitempty"`
	Execution         ExecutionPolicy `json:"execution"`
}

func ValidContainerMountTarget(target string) bool {
	return strings.HasPrefix(target, "/") && target != "/" && !strings.ContainsAny(target, ",\x00\r\n") && filepath.ToSlash(filepath.Clean(target)) == target
}
