package toolhub

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

type WorkloadWorkspace struct {
	Path    string
	Cleanup func() error
}

// OpenWorkloadWorkspace creates only the state that the resolved workload is
// allowed to own. Per-user paths are stable across restart; per-job paths are
// temporary and must be cleaned by the returned function on every terminal
// outcome. Shared workloads receive no filesystem state.
func OpenWorkloadWorkspace(root string, effective EffectiveBinding, jobID string) (WorkloadWorkspace, error) {
	root, err := filepath.Abs(root)
	if err != nil || root == "" {
		return WorkloadWorkspace{}, fmt.Errorf("%w: workload root", ErrInvalid)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return WorkloadWorkspace{}, err
	}
	if err := noSymlinkPath(root); err != nil {
		return WorkloadWorkspace{}, err
	}
	definitionID := effective.Definition.DefinitionID
	switch effective.Definition.Workload.Class {
	case Shared:
		if jobID != "" {
			return WorkloadWorkspace{}, fmt.Errorf("%w: shared workload cannot have job state", ErrUnauthorized)
		}
		return WorkloadWorkspace{Cleanup: func() error { return nil }}, nil
	case PerUser:
		if jobID != "" || !identity.ValidID(effective.Binding.ContextID) {
			return WorkloadWorkspace{}, fmt.Errorf("%w: per-user context", ErrInvalid)
		}
		var path string
		if effective.Connection != nil {
			path = filepath.Join(root, "per-user", effective.Binding.PrincipalID, effective.Binding.ContextID, definitionID, CredentialReferenceID(effective.Connection.ConnectionID, 0))
		} else {
			if !identity.ValidID(effective.Binding.ToolBindingID) || !identity.ValidID(effective.Binding.PrincipalID) {
				return WorkloadWorkspace{}, fmt.Errorf("%w: anonymous binding identity", ErrInvalid)
			}
			path = filepath.Join(root, "per-binding", effective.Binding.PrincipalID, effective.Binding.ContextID, definitionID, effective.Binding.ToolBindingID)
		}
		if err := createOwnedDirectory(root, path); err != nil {
			return WorkloadWorkspace{}, err
		}
		return WorkloadWorkspace{Path: path, Cleanup: func() error { return nil }}, nil
	case PerJob:
		if !identity.ValidID(effective.Binding.ContextID) || !identity.ValidID(jobID) {
			return WorkloadWorkspace{}, fmt.Errorf("%w: per-job identity", ErrInvalid)
		}
		parent := filepath.Join(root, "per-job", effective.Binding.ContextID)
		if err := createOwnedDirectory(root, parent); err != nil {
			return WorkloadWorkspace{}, err
		}
		path, err := os.MkdirTemp(parent, jobID+"-")
		if err != nil {
			return WorkloadWorkspace{}, err
		}
		if err := noSymlinkPath(path); err != nil {
			_ = os.RemoveAll(path)
			return WorkloadWorkspace{}, err
		}
		return WorkloadWorkspace{Path: path, Cleanup: func() error {
			if !containedPath(root, path) || filepath.Clean(path) == filepath.Clean(root) {
				return fmt.Errorf("%w: unsafe job cleanup path", ErrUnauthorized)
			}
			return os.RemoveAll(path)
		}}, nil
	default:
		return WorkloadWorkspace{}, fmt.Errorf("%w: workload class", ErrInvalid)
	}
}

func createOwnedDirectory(root, path string) error {
	if !containedPath(root, path) || strings.Contains(filepath.Base(path), "..") {
		return fmt.Errorf("%w: workload path", ErrUnauthorized)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return noSymlinkPath(path)
}
