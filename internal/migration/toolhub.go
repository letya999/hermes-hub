package migration

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

type ToolHubMigrationOptions struct {
	Directory string
	StateDir  string
	StorePath string
	User      string
	Context   string
	Apply     bool
}

type ToolHubMigrationReport struct {
	Schema              int      `json:"schema"`
	Mode                string   `json:"mode"`
	StorePath           string   `json:"store_path"`
	RollbackPath        string   `json:"rollback_path,omitempty"`
	SettingsRead        bool     `json:"settings_read"`
	ScopeRead           bool     `json:"scope_read"`
	GeneratedMCPPresent bool     `json:"generated_mcp_present"`
	LegacyEnabled       []string `json:"legacy_enabled,omitempty"`
	LegacyDisabled      []string `json:"legacy_disabled,omitempty"`
	ImportedManifests   []string `json:"imported_manifests,omitempty"`
	ImportedBindings    []string `json:"imported_bindings,omitempty"`
	Unmatched           []string `json:"unmatched,omitempty"`
	SecretsCopied       bool     `json:"secrets_copied"`
	Applied             bool     `json:"applied"`
	RollbackOnFailure   bool     `json:"rollback_on_failure"`
}

// MigrateToolHub imports only data whose shape is already explicit in the
// current settings. It does not guess remote tool lists or copy secret values.
func MigrateToolHub(options ToolHubMigrationOptions) (ToolHubMigrationReport, error) {
	if options.Directory == "" || options.User == "" {
		return ToolHubMigrationReport{}, errors.New("migration directory and user are required")
	}
	settings, err := stack.Read(filepath.Join(options.Directory, "settings.yaml"))
	if err != nil {
		return ToolHubMigrationReport{}, fmt.Errorf("read settings: %w", err)
	}
	if options.Context == "" {
		options.Context = options.User
	}
	if options.StateDir == "" {
		options.StateDir = filepath.Join(options.Directory, "runtime")
	}
	if options.StorePath == "" {
		options.StorePath = filepath.Join(options.StateDir, "toolhub", "store.json")
	}
	if !filepath.IsAbs(options.StorePath) {
		return ToolHubMigrationReport{}, errors.New("ToolHub store path must be absolute")
	}
	report := ToolHubMigrationReport{Schema: 1, Mode: "dry-run", StorePath: options.StorePath, SettingsRead: true, ScopeRead: fileExists(filepath.Join(options.Directory, "scope.yaml")), GeneratedMCPPresent: generatedMCPPresent(options.Directory)}
	if options.Apply {
		report.Mode = "apply"
	}
	legacy, err := readLegacyServices(filepath.Join(options.StateDir, "self-services.json"))
	if err != nil {
		return report, err
	}
	report.LegacyEnabled = legacy
	report.LegacyDisabled = append([]string(nil), settings.DisabledMCP...)
	slices.Sort(report.LegacyDisabled)

	store := toolhub.NewStore()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: options.User, ExternalIdentityID: options.User, ContextID: options.Context, RuntimeID: options.User, ConversationID: "migration", DeliveryTargetID: "migration", PolicyVersion: "policy-1"}
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return report, err
	}
	for name, server := range settings.MCP {
		definition, connection, reference, binding, reason, err := importMCP(name, server, settings.DisabledMCP, auth)
		if err != nil {
			return report, err
		}
		if reason != "" {
			report.Unmatched = append(report.Unmatched, name+": "+reason)
			continue
		}
		if err := store.RegisterDefinition(definition); err != nil {
			return report, err
		}
		if reference != nil {
			if err := store.PutCredentialReference(*reference); err != nil {
				return report, err
			}
		}
		if connection != nil {
			if err := store.PutConnection(*connection); err != nil {
				return report, err
			}
		}
		if err := store.PutBinding(binding); err != nil {
			return report, err
		}
		report.ImportedManifests = append(report.ImportedManifests, definition.DefinitionID+"@"+definition.Version)
		report.ImportedBindings = append(report.ImportedBindings, binding.ToolBindingID)
	}
	slices.Sort(report.ImportedManifests)
	slices.Sort(report.ImportedBindings)
	for _, feature := range report.LegacyEnabled {
		report.Unmatched = append(report.Unmatched, feature+": generated service manifest needs explicit tool list")
	}
	slices.Sort(report.Unmatched)

	if !options.Apply {
		return report, nil
	}
	if err := applyToolHubStore(store, options.StorePath, &report); err != nil {
		return report, err
	}
	report.Applied = true
	return report, nil
}

func importMCP(name string, server stack.MCPServer, disabled []string, auth identity.Envelope) (toolhub.ToolDefinition, *toolhub.Connection, *toolhub.CredentialReference, toolhub.ToolBinding, string, error) {
	if server.Tools == nil || len(server.Tools.Include) == 0 {
		return toolhub.ToolDefinition{}, nil, nil, toolhub.ToolBinding{}, "explicit tools.include is required; discovery is not migration evidence", nil
	}
	definition := toolhub.ToolDefinition{Schema: toolhub.SchemaVersion, DefinitionID: name, Version: "1.0.0", Tools: make([]toolhub.ToolSpec, 0, len(server.Tools.Include)), Workload: toolhub.WorkloadPolicy{Class: toolhub.PerUser, Rationale: "legacy owner-scoped MCP connection"}, Execution: toolhub.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32}, Health: toolhub.HealthProbe{TimeoutSeconds: 5}}
	for _, toolName := range server.Tools.Include {
		if !regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`).MatchString(toolName) {
			return toolhub.ToolDefinition{}, nil, nil, toolhub.ToolBinding{}, "invalid explicit tool name", nil
		}
		definition.Tools = append(definition.Tools, toolhub.ToolSpec{Name: toolName, Effect: toolhub.ReadEffect})
	}
	slices.SortFunc(definition.Tools, func(a, b toolhub.ToolSpec) int { return strings.Compare(a.Name, b.Name) })
	var host string
	keys := credentialKeys(server)
	if server.URL != "" {
		definition.Transport = toolhub.RemoteMCP
		definition.Source = toolhub.DefinitionSource{URL: server.URL, TLSMode: "required"}
		definition.Health.Kind, definition.Health.Value = "http", "/health"
		if parsedHost, err := hostFromURL(server.URL); err != nil {
			return toolhub.ToolDefinition{}, nil, nil, toolhub.ToolBinding{}, "invalid HTTPS URL", nil
		} else {
			host = parsedHost
		}
	} else {
		definition.Transport = toolhub.BoundedCLI
		definition.Source = toolhub.DefinitionSource{Command: server.Command, Args: append([]string(nil), server.Args...)}
		definition.Workload.Class = toolhub.PerJob
		definition.Health.Kind, definition.Health.Value = "exec", server.Command
		host = "127.0.0.1"
	}
	definition.Execution.Egress = []string{host}
	definition.Credentials = make([]toolhub.CredentialInput, 0, len(keys))
	for _, key := range keys {
		definition.Credentials = append(definition.Credentials, toolhub.CredentialInput{Name: key, Required: true, PerRequest: definition.Workload.Class == toolhub.Shared})
	}
	connectionID := stableConnectionID(name)
	var reference *toolhub.CredentialReference
	var connection *toolhub.Connection
	if len(keys) > 0 {
		ref := toolhub.CredentialReference{Schema: toolhub.SchemaVersion, CredentialRefID: toolhub.CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: "legacy-env", Locator: "runtime-env", Keys: keys, Status: toolhub.ActiveStatus}
		reference = &ref
		conn := toolhub.Connection{Schema: toolhub.SchemaVersion, ConnectionID: connectionID, Owner: toolhub.OwnerRef{Type: toolhub.PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: name, CredentialRefID: ref.CredentialRefID, Revision: 1, Status: toolhub.ActiveStatus, Metadata: map[string]string{"legacy_name": name}}
		connection = &conn
	}
	status := toolhub.ActiveStatus
	if slices.Contains(disabled, name) {
		status = toolhub.DisabledStatus
	}
	binding := toolhub.ToolBinding{Schema: toolhub.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: name, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: status, Revision: 1, ProjectionRevision: 1}
	if connection != nil {
		binding.ConnectionID, binding.ConnectionRevision = connection.ConnectionID, connection.Revision
		binding.CredentialRefID, binding.CredentialRevision = reference.CredentialRefID, reference.Revision
	}
	binding.ToolBindingID = toolhub.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	return definition, connection, reference, binding, "", nil
}

func credentialKeys(server stack.MCPServer) []string {
	seen := map[string]bool{}
	for _, values := range []map[string]string{server.Env, server.Headers} {
		for _, value := range values {
			for _, match := range regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`).FindAllStringSubmatch(value, -1) {
				seen[match[1]] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for key := range seen {
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}

func hostFromURL(raw string) (string, error) {
	if !strings.HasPrefix(raw, "https://") || strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("HTTPS required")
	}
	value := strings.TrimPrefix(raw, "https://")
	if i := strings.IndexAny(value, "/?#"); i >= 0 {
		value = value[:i]
	}
	if value == "" || strings.Contains(value, "@") {
		return "", errors.New("invalid host")
	}
	return strings.ToLower(value), nil
}

func stableConnectionID(name string) string {
	if len(name) <= 29 {
		return name + "-connection"
	}
	hash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("conn-%x", hash[:8])
}

func applyToolHubStore(store *toolhub.Store, path string, report *ToolHubMigrationReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	backup := path + ".rollback"
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, backup); err != nil {
			return fmt.Errorf("create rollback copy: %w", err)
		}
		report.RollbackPath = backup
	}
	if err := store.Save(path); err != nil {
		if report.RollbackPath != "" {
			if restoreErr := os.Rename(report.RollbackPath, path); restoreErr == nil {
				report.RollbackOnFailure = true
			}
		}
		return fmt.Errorf("write ToolHub store; rollback restored: %w", err)
	}
	report.SecretsCopied = false
	return nil
}

func readLegacyServices(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state struct {
		Features []string `json:"features"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, fmt.Errorf("read legacy self-services: %w", err)
	}
	slices.Sort(state.Features)
	return slices.Compact(state.Features), nil
}

func generatedMCPPresent(directory string) bool {
	entries, err := os.ReadDir(filepath.Join(directory, "generated"))
	return err == nil && len(entries) > 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
