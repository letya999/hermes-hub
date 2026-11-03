package agenttools

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// workspaceBoundary is the writable/read_only contract published in
// /config/workspace.yaml. File admission reloads it by mtime: a narrowed
// revision lands on the next call without a respawn; an absent or
// undecodable file fails closed (SPEC-0092 R5.3).
type workspaceBoundary struct {
	writable []string
	readOnly []string
}

type boundaryDoc struct {
	Writable []string `yaml:"writable"`
	ReadOnly []string `yaml:"read_only"`
}

// boundary returns the live workspace boundary when BoundaryPath is set,
// cached by file mtime+size so an apply swaps it on the next call. An
// empty BoundaryPath means this runtime has no published workspace
// passport — the caller keeps the legacy HUB_WORKSPACE_RO semantics.
func (t *Tools) boundary() (*workspaceBoundary, error) {
	if t.BoundaryPath == "" {
		return nil, nil
	}
	info, err := os.Stat(t.BoundaryPath)
	if err != nil {
		t.boundaryValid = false
		return nil, fmt.Errorf("workspace boundary: %w", err)
	}
	if t.boundaryValid && t.boundaryMod.Equal(info.ModTime()) && t.boundarySize == info.Size() {
		return t.boundaryVal, t.boundaryErr
	}
	t.boundaryMod, t.boundarySize = info.ModTime(), info.Size()
	body, err := os.ReadFile(t.BoundaryPath)
	if err != nil {
		t.boundaryVal, t.boundaryErr, t.boundaryValid = nil, fmt.Errorf("workspace boundary: %w", err), true
		return nil, t.boundaryErr
	}
	var doc boundaryDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.boundaryVal, t.boundaryErr, t.boundaryValid = nil, fmt.Errorf("workspace boundary: %w", err), true
		return nil, t.boundaryErr
	}
	t.boundaryVal = &workspaceBoundary{writable: doc.Writable, readOnly: doc.ReadOnly}
	t.boundaryErr, t.boundaryValid = nil, true
	return t.boundaryVal, nil
}

// denyWrite applies the boundary to one absolute path: the longest
// matching prefix decides — a read_only prefix at equal or greater depth
// wins over any writable ancestor, and nothing outside writable is
// admitted.
func (b *workspaceBoundary) denyWrite(abs string) error {
	best := 0
	writable := false
	for _, p := range b.writable {
		if abs == p || strings.HasPrefix(abs, p+"/") {
			if len(p) >= best {
				best, writable = len(p), true
			}
		}
	}
	for _, p := range b.readOnly {
		if abs == p || strings.HasPrefix(abs, p+"/") {
			if len(p) >= best {
				return fmt.Errorf("%s is read-only", abs)
			}
		}
	}
	if !writable {
		return fmt.Errorf("%s is outside the writable workspace boundary", abs)
	}
	return nil
}
