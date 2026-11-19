package agenttools

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/shared/locale"
)

// workspaceBoundary is the writable/read_only contract published in
// /config/workspace.yaml. File admission reloads it on every write: a narrowed
// revision lands on the next call without a respawn; an absent or
// undecodable file fails closed (SPEC-0092 R5.3).
type workspaceBoundary struct {
	writable []string
	readOnly []string
}

// Managed executors require a strictly decoded, active workspace passport.
// Component-only callers without a managed identity may omit the boundary.
func (t *Tools) boundary() (*workspaceBoundary, error) {
	if t.BoundaryPath == "" {
		return nil, nil
	}
	invalid := errors.New(locale.T(locale.LangOf(os.Getenv("HUB_WORKSPACE")), "tools.workspace-boundary-invalid"))
	info, err := os.Lstat(t.BoundaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, invalid
	}
	marker := filepath.Join(filepath.Dir(t.BoundaryPath), "revision")
	revision, err := os.ReadFile(marker)
	if err != nil || len(revision) == 0 || len(revision) > 128 {
		return nil, invalid
	}
	body, err := os.ReadFile(t.BoundaryPath)
	if err != nil {
		return nil, invalid
	}
	decoded, err := passportview.Decode("workspace", body)
	if err != nil {
		return nil, invalid
	}
	doc := decoded.(*passportview.WorkspaceDoc)
	after, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(revision, after) || strings.TrimSpace(string(revision)) != doc.Metadata.Revision {
		return nil, invalid
	}
	return &workspaceBoundary{writable: doc.Writable, readOnly: doc.ReadOnly}, nil
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
