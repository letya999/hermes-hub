// Package secrets is the owner-facing credential service: ciphertext store,
// ToolHub locators, inject-after-authorize, chat intercept and terminal exposure.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

var ErrNotConfigured = errors.New("credential store is not configured")

type Service struct {
	Backend  credstore.Backend
	Registry *toolhub.Store
	Audit    *audit.Ledger
	EnvFile  func(owner string) string
}

type Info struct {
	Name             string
	Status           string
	Locator          string
	Revision         uint64
	TerminalExposure bool
}

type Injection struct {
	Env  map[string]string
	Wipe func() error
}

func Open(storePath, keyFile string, key []byte, backend string) (*Service, error) {
	opts := credstore.Options{Path: storePath, KeyFile: keyFile, Key: key, Backend: backend}
	store, err := credstore.Open(opts)
	if err != nil {
		return nil, err
	}
	return &Service{Backend: store}, nil
}

func (s *Service) Set(owner string, values map[string]string) ([]Info, error) {
	if s == nil || s.Backend == nil {
		return nil, ErrNotConfigured
	}
	if len(values) == 0 {
		return nil, credstore.ErrInvalid
	}
	infos := make([]Info, 0, len(values))
	names := make([]string, 0, len(values))
	for name, value := range values {
		locator, err := s.locatorFor(owner, name)
		if err != nil {
			return nil, err
		}
		if err := s.Backend.Put(locator, owner, map[string]string{name: value}); err != nil {
			return nil, err
		}
		info, err := s.Backend.Info(locator, owner)
		if err != nil {
			return nil, err
		}
		if err := s.audit(audit.NewEvent("credential-change", owner, "set")); err != nil {
			_ = s.Backend.Delete(locator, owner)
			return nil, err
		}
		infos = append(infos, Info{Name: name, Status: info.Status, Locator: locator, Revision: info.Revision, TerminalExposure: info.TerminalExposure})
		names = append(names, name)
		if err := s.publishRegistry(owner, locator, []string{name}, false); err != nil {
			return nil, err
		}
	}
	slices.Sort(names)
	return infos, nil
}

func (s *Service) List(owner string) ([]Info, error) {
	if s == nil || s.Backend == nil {
		return nil, ErrNotConfigured
	}
	records, err := s.Backend.List(owner)
	if err != nil {
		return nil, err
	}
	result := make([]Info, 0)
	for _, record := range records {
		for _, name := range record.Names {
			result = append(result, Info{Name: name, Status: record.Status, Locator: record.Locator, Revision: record.Revision, TerminalExposure: record.TerminalExposure})
		}
	}
	slices.SortFunc(result, func(a, b Info) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

func (s *Service) Delete(owner, name string) error {
	if s == nil || s.Backend == nil {
		return ErrNotConfigured
	}
	locator, err := s.locatorForExisting(owner, name)
	if err != nil {
		return err
	}
	if err := s.Backend.Delete(locator, owner); err != nil {
		return err
	}
	if path := s.envPath(owner); path != "" {
		_, _ = envstore.Remove(path, name, "", name)
	}
	event := audit.NewEvent("credential-change", owner, "delete")
	event.Name = name
	if err := s.audit(event); err != nil {
		return err
	}
	return s.publishRegistry(owner, locator, []string{name}, true)
}

func (s *Service) Rotate(owner, name, value, connectionID string) error {
	if s == nil || s.Backend == nil {
		return ErrNotConfigured
	}
	locator, err := s.locatorForExisting(owner, name)
	if err != nil {
		return err
	}
	if err := s.Backend.Put(locator, owner, map[string]string{name: value}); err != nil {
		_ = s.Backend.SetStatus(locator, owner, credstore.StatusDegraded)
		if s.Registry != nil && connectionID != "" {
			_ = s.Registry.MarkDegraded(connectionID)
		}
		return err
	}
	if s.Registry != nil && connectionID != "" {
		if _, err := s.Registry.RotateCredential(connectionID, s.Backend.Name(), locator, []string{name}); err != nil {
			_ = s.Registry.MarkDegraded(connectionID)
			_ = s.Backend.SetStatus(locator, owner, credstore.StatusDegraded)
			return err
		}
	}
	event := audit.NewEvent("credential-change", owner, "rotate")
	event.Name = name
	if err := s.audit(event); err != nil {
		if s.Registry != nil && connectionID != "" {
			_ = s.Registry.MarkDegraded(connectionID)
		}
		return err
	}
	return nil
}

func (s *Service) SetTerminalExposure(owner, name string, enabled bool) error {
	if s == nil || s.Backend == nil {
		return ErrNotConfigured
	}
	locator, err := s.locatorForExisting(owner, name)
	if err != nil {
		return err
	}
	if err := s.Backend.SetTerminalExposure(locator, owner, enabled); err != nil {
		return err
	}
	path := s.envPath(owner)
	if enabled {
		values, err := s.Backend.Get(locator, owner)
		if err != nil {
			return err
		}
		if path != "" {
			if _, err := envstore.Update(path, name+"="+values[name], name, ""); err != nil {
				return err
			}
		}
	} else if path != "" {
		_, _ = envstore.Remove(path, name, "", name)
	}
	event := audit.NewEvent("terminal-exposure", owner, statusText(enabled))
	event.Name = name
	return s.audit(event)
}

func (s *Service) ApplyChat(owner, text string) ([]string, string, error) {
	values, err := envstore.Parse(text)
	if err != nil {
		return nil, "Статус: rejected", err
	}
	if len(values) == 0 {
		return nil, "Статус: rejected", credstore.ErrInvalid
	}
	infos, err := s.Set(owner, values)
	if err != nil {
		return nil, "Статус: rejected", err
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names, FormatStatus(names, "active"), nil
}

func (s *Service) Inject(auth identity.Envelope, projectedName, workloadRoot, jobID string) (Injection, error) {
	if s == nil || s.Backend == nil || s.Registry == nil {
		return Injection{}, ErrNotConfigured
	}
	var injected Injection
	err := s.Registry.AuthorizeProjected(auth, projectedName, func(_ toolhub.ProjectedTool, effective toolhub.EffectiveBinding) error {
		if effective.Definition.Workload.Class == toolhub.Shared {
			for _, input := range effective.Definition.Credentials {
				if !input.PerRequest {
					return fmt.Errorf("%w: static credentials cannot be shared", toolhub.ErrUnauthorized)
				}
			}
		}
		injected.Env = map[string]string{}
		injected.Wipe = func() error { return nil }
		if effective.Credential == nil {
			return nil
		}
		owner := auth.PrincipalID
		if effective.Connection != nil {
			owner = effective.Connection.Owner.ID
		}
		values, err := s.Backend.Get(effective.Credential.Locator, owner)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, key := range effective.Credential.Keys {
			allowed[key] = true
		}
		for key, value := range values {
			if !allowed[key] {
				continue
			}
			injected.Env[key] = value
		}
		workspace, err := toolhub.OpenWorkloadWorkspace(workloadRoot, effective, jobID)
		if err != nil {
			return err
		}
		if workspace.Path != "" && len(injected.Env) > 0 {
			if err := toolhub.WriteWorkloadEnvFile(workspace.Path, injected.Env); err != nil {
				_ = workspace.Cleanup()
				return err
			}
		}
		injected.Wipe = func() error {
			if workspace.Path != "" {
				_ = os.Remove(filepath.Join(workspace.Path, "credentials.env"))
			}
			return workspace.Cleanup()
		}
		event := audit.NewEvent("tool-call", auth.PrincipalID, "inject")
		event.RuntimeID = auth.RuntimeID
		event.ContextID = auth.ContextID
		event.PolicyRevision = auth.PolicyVersion
		if effective.Connection != nil {
			event.ConnectionID = effective.Connection.ConnectionID
		}
		if effective.Credential != nil {
			event.CredentialRevision = effective.Credential.Revision
		}
		event.ProjectionRevision = effective.Binding.ProjectionRevision
		return s.audit(event)
	})
	if err != nil {
		if injected.Wipe != nil {
			_ = injected.Wipe()
		}
		return Injection{}, err
	}
	return injected, nil
}

func FormatStatus(names []string, status string) string {
	slices.Sort(names)
	return "Имена: " + strings.Join(names, ", ") + ". Статус: " + status + "."
}

func ContainsValue(text string, values map[string]string) bool {
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func (s *Service) locatorFor(owner, name string) (string, error) {
	records, err := s.Backend.List(owner)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if slices.Contains(record.Names, name) {
			return record.Locator, nil
		}
	}
	return s.Backend.NewLocator()
}

func (s *Service) locatorForExisting(owner, name string) (string, error) {
	records, err := s.Backend.List(owner)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if slices.Contains(record.Names, name) {
			return record.Locator, nil
		}
	}
	return "", credstore.ErrNotFound
}

func (s *Service) envPath(owner string) string {
	if s != nil && s.EnvFile != nil {
		return s.EnvFile(owner)
	}
	return ""
}

func (s *Service) audit(event audit.Event) error {
	if s == nil || s.Audit == nil {
		return nil
	}
	if event.EventID == "" {
		event.EventID = "evt-missing"
	}
	return s.Audit.Append(event)
}

func (s *Service) publishRegistry(owner, locator string, names []string, revoke bool) error {
	if s == nil || s.Registry == nil {
		return nil
	}
	refs := s.Registry.ConnectionsForSecret(locator, owner, names)
	for _, ref := range refs {
		if revoke {
			if err := s.Registry.SetConnectionStatus(ref.ConnectionID, toolhub.RevokedStatus); err != nil {
				_ = s.Registry.MarkDegraded(ref.ConnectionID)
				return err
			}
			continue
		}
		keys := ref.Keys
		if len(keys) == 0 {
			keys = names
		}
		if _, err := s.Registry.RotateCredential(ref.ConnectionID, s.Backend.Name(), locator, keys); err != nil {
			_ = s.Registry.MarkDegraded(ref.ConnectionID)
			_ = s.Backend.SetStatus(locator, owner, credstore.StatusDegraded)
			return err
		}
	}
	return nil
}

func statusText(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
