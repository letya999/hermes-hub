// Package migration moves legacy host and runtime data into peer scope homes.
package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

type Options struct {
	Root               string
	User               string
	Organization       string
	Environment        string
	Apply              bool
	UserSource         string
	OrganizationSource string
	StateSource        string
	WorkspaceSource    string
}

type Object struct {
	Path   string `json:"path"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Report struct {
	Mode         string   `json:"mode"`
	User         string   `json:"user"`
	Organization string   `json:"organization,omitempty"`
	Destination  []string `json:"destination"`
	Sources      []string `json:"sources"`
	Objects      []Object `json:"objects"`
	Rollback     string   `json:"rollback"`
}

func Run(opts Options) (Report, error) {
	var report Report
	if opts.Root == "" {
		opts.Root = "."
	}
	if opts.Environment == "" {
		opts.Environment = "prod"
	}
	if opts.Environment != "dev" && opts.Environment != "prod" {
		return report, errors.New("environment must be dev or prod")
	}
	if err := validateID(opts.User); err != nil {
		return report, fmt.Errorf("user: %w", err)
	}
	if opts.Organization != "" {
		if err := validateID(opts.Organization); err != nil {
			return report, fmt.Errorf("organization: %w", err)
		}
		if opts.Organization == opts.User {
			return report, errors.New("organization and user IDs collide")
		}
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return report, err
	}
	spaces := filepath.Join(root, "spaces")
	userDest := filepath.Join(spaces, opts.User)
	orgDest := ""
	if opts.Organization != "" {
		orgDest = filepath.Join(spaces, opts.Organization)
	}
	userSource := opts.UserSource
	if userSource == "" {
		userSource = userDest
	}
	orgSource := opts.OrganizationSource
	if orgSource == "" && opts.Organization != "" {
		orgSource = filepath.Join(root, "organizations", opts.Organization)
	}
	for _, p := range []string{userSource, orgSource} {
		if p == "" {
			continue
		}
		if err := rejectSymlinks(p); err != nil {
			return report, err
		}
	}
	if err := validateDestination(userDest, stack.UserScope, opts.User); err != nil {
		return report, err
	}
	if orgDest != "" {
		if err := validateDestination(orgDest, stack.OrganizationScope, opts.Organization); err != nil {
			return report, err
		}
	}
	if opts.UserSource != "" && !samePath(userSource, userDest) {
		if err := requireDir(userSource); err != nil {
			return report, err
		}
	}
	if orgSource != "" {
		if _, err := os.Stat(orgSource); err != nil && !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
	}
	if opts.StateSource != "" {
		if err := requireDir(opts.StateSource); err != nil {
			return report, err
		}
		if err := rejectSymlinks(opts.StateSource); err != nil {
			return report, err
		}
		if activeRuntime(opts.StateSource) {
			return report, errors.New("migration refused while the selected runtime is active")
		}
	}
	if opts.WorkspaceSource != "" {
		if err := requireDir(opts.WorkspaceSource); err != nil {
			return report, err
		}
		if err := rejectSymlinks(opts.WorkspaceSource); err != nil {
			return report, err
		}
	}
	if opts.StateSource != "" {
		report.Sources = append(report.Sources, opts.StateSource)
	}
	if opts.WorkspaceSource != "" {
		report.Sources = append(report.Sources, opts.WorkspaceSource)
	}
	if opts.Organization != "" && orgSource != "" {
		if _, err := os.Stat(orgSource); err == nil {
			report.Sources = append(report.Sources, orgSource)
		}
	}
	if !samePath(userSource, userDest) {
		if _, err := os.Stat(userSource); err == nil {
			report.Sources = append(report.Sources, userSource)
		}
	}
	for _, source := range report.Sources {
		object, err := inventory(source)
		if err != nil {
			return report, err
		}
		report.Objects = append(report.Objects, object)
	}
	report.User, report.Organization = opts.User, opts.Organization
	report.Destination = []string{userDest}
	if orgDest != "" {
		report.Destination = append(report.Destination, orgDest)
	}
	report.Mode = "dry-run"
	report.Rollback = "Sources remain at their original paths; remove the activated space only after verifying the report."
	if !opts.Apply {
		return report, nil
	}
	if err := activate(&report, opts, userDest, orgDest, userSource, orgSource); err != nil {
		return Report{}, err
	}
	report.Mode = "applied"
	return report, nil
}

func validateID(id string) error {
	if id == "" {
		return errors.New("identifier is required")
	}
	for i, r := range id {
		if !(r >= 'a' && r <= 'z') && !(i > 0 && r >= '0' && r <= '9') && !(i > 0 && r == '_') && !(i > 0 && r == '-') {
			return errors.New("identifier must be a lowercase path-safe value")
		}
		if i >= 40 {
			return errors.New("identifier is too long")
		}
	}
	return nil
}

func requireDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("source %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %s is not a directory", path)
	}
	return nil
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(aa) == filepath.Clean(bb)
}

func rejectSymlinks(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink source is not allowed: %s", root)
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink source is not allowed: %s", path)
		}
		return nil
	})
}

func validateDestination(path string, kind stack.ScopeKind, id string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination is a symlink: %s", path)
	}
	if scope, scopeErr := stack.ReadScope(path); scopeErr == nil {
		if scope.Kind != kind || scope.ID != id {
			return fmt.Errorf("destination scope collision at %s", path)
		}
	}
	allowed := map[string]bool{"scope.yaml": true, "settings.yaml": true, "SOUL.md": true, "secrets.dev.env": true, "secrets.prod.env": true, "docs": true, "hermes": true, "connections": true, "workspace": true, "archive": true, "generated": true}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("destination contains unrelated data: %s", filepath.Join(path, entry.Name()))
		}
	}
	return nil
}

func inventory(root string) (Object, error) {
	object := Object{Path: root}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return object, nil
	} else if err != nil {
		return object, err
	}
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink source is not allowed: %s", path)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		object.Files++
		object.Bytes += n
		return nil
	})
	if err != nil {
		return object, err
	}
	object.SHA256 = hex.EncodeToString(h.Sum(nil))
	return object, nil
}

func activeRuntime(path string) bool {
	body, err := os.ReadFile(filepath.Join(path, "runtime.json"))
	if err != nil {
		return false
	}
	var marker struct {
		PIDs []int `json:"pids"`
	}
	return json.Unmarshal(body, &marker) == nil && len(marker.PIDs) > 0 && marker.PIDs[0] > 1
}

func activate(report *Report, opts Options, userDest, orgDest, userSource, orgSource string) error {
	if err := os.MkdirAll(filepath.Dir(userDest), 0700); err != nil {
		return err
	}
	if err := activateScope(userDest, userSource, stack.UserScope, opts.User, opts.StateSource, opts.WorkspaceSource); err != nil {
		return err
	}
	if orgDest != "" && orgSource != "" {
		if _, err := os.Stat(orgSource); err == nil {
			if err := activateScope(orgDest, orgSource, stack.OrganizationScope, opts.Organization, "", ""); err != nil {
				return err
			}
		}
	}
	_ = report
	return nil
}

func activateScope(destination, source string, kind stack.ScopeKind, id, stateSource, workspaceSource string) error {
	if samePath(source, destination) {
		if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(destination, 0700); err != nil {
				return err
			}
		}
		if err := addScopeFiles(destination, kind, id); err != nil {
			return err
		}
		if stateSource != "" {
			if err := copyRuntimeState(stateSource, destination); err != nil {
				return err
			}
		}
		if workspaceSource != "" {
			if err := copyTree(workspaceSource, filepath.Join(destination, "workspace")); err != nil {
				return err
			}
		}
		return ensureDirs(destination, kind)
	}
	stage := filepath.Join(filepath.Dir(destination), fmt.Sprintf(".migration-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(stage, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := copyTree(source, stage); err != nil {
		return err
	}
	if err := addScopeFiles(stage, kind, id); err != nil {
		return err
	}
	if stateSource != "" {
		if err := copyRuntimeState(stateSource, stage); err != nil {
			return err
		}
	}
	if workspaceSource != "" {
		if err := copyTree(workspaceSource, filepath.Join(stage, "workspace")); err != nil {
			return err
		}
	}
	if err := ensureDirs(stage, kind); err != nil {
		return err
	}
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		return os.Rename(stage, destination)
	}
	return copyTree(stage, destination)
}

func addScopeFiles(dir string, kind stack.ScopeKind, id string) error {
	if kind == stack.UserScope {
		path := filepath.Join(dir, "scope.yaml")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return os.WriteFile(path, []byte("kind: user\nid: "+id+"\n"), 0600)
		}
		return nil
	}
	path := filepath.Join(dir, "scope.yaml")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if organization, err := stack.ReadOrganization(dir); err == nil {
			organization.Kind, organization.ID = stack.OrganizationScope, id
			body, marshalErr := yaml.Marshal(organization)
			if marshalErr != nil {
				return marshalErr
			}
			return os.WriteFile(path, body, 0600)
		}
		return os.WriteFile(path, []byte("kind: organization\nid: "+id+"\norganization: "+id+"\n"), 0600)
	}
	return nil
}

func ensureDirs(dir string, kind stack.ScopeKind) error {
	paths := []string{"hermes/memories", "hermes/skills", "hermes/sessions", "hermes/hooks", "hermes/plugins", "connections", "workspace", "archive", "generated"}
	if kind == stack.OrganizationScope {
		paths = append(paths, "docs")
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Join(dir, path), 0700); err != nil {
			return err
		}
	}
	return nil
}

func copyRuntimeState(source, destination string) error {
	for _, item := range []struct{ from, to string }{
		{"hermes", "hermes"}, {"browser", "connections/browser"}, {"google", "connections/google"}, {"telegram", "connections/telegram"}, {"self-env.json", "connections/self-env.json"}, {"self-services.json", "connections/self-services.json"},
	} {
		from := filepath.Join(source, item.from)
		if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := copyTree(from, filepath.Join(destination, item.to)); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(source, destination string) error {
	if _, err := os.Stat(source); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink source is not allowed: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return atomicWrite(target, body)
	})
}

func atomicWrite(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".migration-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
