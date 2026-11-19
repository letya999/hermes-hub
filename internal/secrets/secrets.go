// Package secrets is the owner-facing credential service: ciphertext store,
// ToolHub locators, inject-after-authorize, chat intercept and terminal exposure.
package secrets

import (
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/shared/locale"
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

func (s *Service) Set(owner string, values map[string]string) (result []Info, resultErr error) {
	if s == nil || s.Backend == nil {
		return nil, ErrNotConfigured
	}
	if len(values) == 0 {
		return nil, credstore.ErrInvalid
	}
	names := make([]string, 0, len(values))
	for name, value := range values {
		if err := credstore.ValidateValue(name, value); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	slices.Sort(names)
	ticket, err := s.beginMutation(mutationEvent(owner, "credential-change", "credential.set"))
	if err != nil {
		return nil, err
	}
	locators := make([]string, len(names))
	attempted := 0
	defer func() {
		ticket.finish(&resultErr)
		if resultErr != nil {
			result = nil
			for i := 0; i < attempted; i++ {
				_ = s.degrade(owner, locators[i], []string{names[i]})
			}
		}
	}()
	// Resolve under durable owner admission so concurrent batches cannot reuse a stale lookup.
	for i, name := range names {
		locators[i], err = s.locatorFor(owner, name)
		if err != nil {
			return nil, err
		}
	}
	for i, name := range names {
		replacement, err := s.replacementValues(owner, locators[i], name, values[name])
		if err != nil {
			return nil, err
		}
		ticket.execution = "unknown"
		attempted = i + 1
		if err := s.Backend.Put(locators[i], owner, replacement); err != nil {
			return nil, err
		}
		info, err := s.Backend.Info(locators[i], owner)
		if err != nil {
			return nil, err
		}
		if err := s.publishRegistry(owner, locators[i], []string{name}, false); err != nil {
			return nil, err
		}
		result = append(result, Info{Name: name, Status: info.Status, Locator: locators[i], Revision: info.Revision, TerminalExposure: info.TerminalExposure})
	}
	return result, nil
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
	ticket, admissionErr := s.beginMutation(mutationEvent(owner, "credential-change", "credential.delete"))
	// Requested revocation still stops every access path when audit or one cleanup leg fails.
	effectErr := s.Backend.Delete(locator, owner)
	if path := s.envPath(owner); path != "" {
		_, err = envstore.Remove(path, name, "", name)
		effectErr = errors.Join(effectErr, err)
	}
	effectErr = errors.Join(effectErr, s.publishRegistry(owner, locator, []string{name}, true))
	if admissionErr != nil {
		return audit.ErrUncertain
	}
	ticket.execution = "unknown"
	ticket.finish(&effectErr)
	return effectErr
}

func (s *Service) Rotate(owner, name, value, connectionID string) (resultErr error) {
	if s == nil || s.Backend == nil {
		return ErrNotConfigured
	}
	locator, err := s.locatorForExisting(owner, name)
	if err != nil {
		return err
	}
	if s.Registry != nil && connectionID != "" {
		owned := false
		for _, ref := range s.Registry.ConnectionsForSecret(locator, owner, []string{name}) {
			if ref.ConnectionID == connectionID {
				owned = true
				break
			}
		}
		if !owned {
			return api.ErrUnauthorized
		}
	}
	ticket, err := s.beginMutation(mutationEvent(owner, "credential-change", "credential.rotate"))
	if err != nil {
		return err
	}
	defer func() {
		ticket.finish(&resultErr)
		if resultErr != nil && ticket.execution == "unknown" {
			_ = s.degrade(owner, locator, []string{name})
		}
	}()
	if invalidErr := credstore.ValidateValue(name, value); invalidErr != nil {
		ticket.execution = "unknown"
		if err := s.degrade(owner, locator, []string{name}); err != nil {
			return audit.ErrUncertain
		}
		ticket.execution = "failed"
		return invalidErr
	}
	replacement, err := s.replacementValues(owner, locator, name, value)
	if err != nil {
		return err
	}
	ticket.execution = "unknown"
	if err := s.Backend.Put(locator, owner, replacement); err != nil {
		return err
	}
	if s.Registry != nil && connectionID != "" {
		if _, err := s.Registry.RotateCredential(connectionID, s.Backend.Name(), locator, []string{name}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SetTerminalExposure(owner, name string, enabled bool) (resultErr error) {
	if s == nil || s.Backend == nil {
		return ErrNotConfigured
	}
	locator, err := s.locatorForExisting(owner, name)
	if err != nil {
		return err
	}
	action := "credential.isolate"
	if enabled {
		action = "credential.expose"
	}
	ticket, admissionErr := s.beginMutation(mutationEvent(owner, "terminal-exposure", action))
	if admissionErr != nil && enabled {
		return admissionErr
	}
	defer func() {
		if ticket == nil {
			resultErr = audit.ErrUncertain
			return
		}
		ticket.finish(&resultErr)
	}()
	if ticket != nil {
		ticket.execution = "unknown"
	}
	flagErr := s.Backend.SetTerminalExposure(locator, owner, enabled)
	path := s.envPath(owner)
	if !enabled {
		if path != "" {
			_, err = envstore.Remove(path, name, "", name)
		}
		return errors.Join(flagErr, err)
	}
	if flagErr != nil {
		return flagErr
	}
	values, err := s.Backend.Get(locator, owner)
	if err != nil {
		return err
	}
	if path != "" {
		_, err = envstore.Update(path, name+"="+values[name], name, "")
	}
	return err
}

func (s *Service) ApplyChat(owner, text string) ([]string, string, error) {
	values, err := envstore.Parse(text)
	if err != nil {
		return nil, FormatStatus(locale.Operator(), nil, "rejected"), err
	}
	if len(values) == 0 {
		return nil, FormatStatus(locale.Operator(), nil, "rejected"), credstore.ErrInvalid
	}
	infos, err := s.Set(owner, values)
	if err != nil {
		if errors.Is(err, audit.ErrUncertain) {
			return nil, locale.T(locale.Operator(), "secret.uncertain"), err
		}
		return nil, FormatStatus(locale.Operator(), nil, "rejected"), err
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names, FormatStatus(locale.Operator(), names, "active"), nil
}

func (s *Service) Inject(auth identity.Envelope, projectedName, workloadRoot, jobID string) (Injection, error) {
	if s == nil || s.Backend == nil || s.Registry == nil {
		return Injection{}, ErrNotConfigured
	}
	var injected Injection
	err := s.Registry.AuthorizeProjected(auth, projectedName, func(_ api.ProjectedTool, effective api.EffectiveBinding) (callbackErr error) {
		if effective.Definition.Workload.Class == api.Shared && !effective.Definition.Workload.Sharable {
			for _, input := range effective.Definition.Credentials {
				if !input.PerRequest {
					return fmt.Errorf("%w: static credentials cannot be shared", api.ErrUnauthorized)
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
		event.Action, event.CorrelationID = "credential.inject", "legacy-credentials"
		event.Environment, event.Generation = auth.Environment, auth.Generation
		ticket, err := s.beginMutation(event)
		if err != nil {
			return err
		}
		defer ticket.finish(&callbackErr)

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
		ticket.execution = "unknown"
		workspace, err := api.OpenWorkloadWorkspace(workloadRoot, effective, jobID)
		if err != nil {
			return err
		}
		for key, value := range injected.Env {
			if api.FileBoundCredentialNames(effective.Definition)[key] {
				if err := api.WriteWorkloadCredentialFile(workspace.Path, key, value); err != nil {
					_ = workspace.Cleanup()
					return err
				}
				delete(injected.Env, key)
			}
		}
		if workspace.Path != "" && len(injected.Env) > 0 {
			if err := api.WriteWorkloadEnvFile(workspace.Path, injected.Env); err != nil {
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
		return nil
	})
	if err != nil {
		if injected.Wipe != nil {
			_ = injected.Wipe()
		}
		return Injection{}, err
	}
	return injected, nil
}

// FormatStatus renders the operator-facing status line. The status value is
// a stable machine token; only the surrounding labels localize.
func FormatStatus(lang locale.Lang, names []string, status string) string {
	slices.Sort(names)
	if len(names) == 0 {
		return locale.T(lang, "op.status-only", status)
	}
	return locale.T(lang, "op.names-status", strings.Join(names, ", "), status)
}

func ContainsValue(text string, values map[string]string) bool {
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			return true
		}
	}
	return false
}

// Existing multi-key objects retain their other values; unreadable siblings are never replaced.
func (s *Service) replacementValues(owner, locator, name, value string) (map[string]string, error) {
	replacement := map[string]string{name: value}
	info, err := s.Backend.Info(locator, owner)
	if errors.Is(err, credstore.ErrNotFound) {
		return replacement, nil
	}
	if err != nil {
		return nil, err
	}
	if !slices.Contains(info.Names, name) {
		return nil, credstore.ErrInvalid
	}
	if len(info.Names) > 1 {
		values, err := s.Backend.Get(locator, owner)
		if err != nil {
			return nil, err
		}
		replacement = maps.Clone(values)
		replacement[name] = value
	}
	return replacement, nil
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

func (s *Service) publishRegistry(owner, locator string, names []string, revoke bool) error {
	if s == nil || s.Registry == nil {
		return nil
	}
	refs := s.Registry.ConnectionsForSecret(locator, owner, names)
	for _, ref := range refs {
		if revoke {
			if err := s.Registry.SetConnectionStatus(ref.ConnectionID, api.RevokedStatus); err != nil {
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
