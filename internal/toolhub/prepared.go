package toolhub

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Prepared entries are reviewed local policy, never downloaded repository policy.
//
//go:embed prepared/catalog.json
var preparedCatalogJSON []byte

//go:embed prepared/contracts/*.json
var preparedContractFiles embed.FS

// BrokerContract returns reviewed deployment data, never upstream authority.
func (entry PreparedEntry) BrokerContract() (contract.Contract, error) {
	var result contract.Contract
	body, err := preparedContractFiles.ReadFile("prepared/contracts/" + entry.ContractID + ".json")
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if result.ID != entry.ContractID || result.Revision != entry.ContractRevision || result.Validate() != nil {
		return result, fmt.Errorf("%w: prepared Broker contract", ErrInvalid)
	}
	return result, nil
}

type PreparedEntry struct {
	ID                 string                     `json:"id"`
	Name               string                     `json:"name"`
	Source             ArtifactSource             `json:"source"`
	License            string                     `json:"license"`
	Language           string                     `json:"language"`
	Entrypoint         []string                   `json:"entrypoint"`
	Connection         ConnectionRecipe           `json:"connection"`
	ContractID         string                     `json:"contract_id"`
	ContractRevision   int                        `json:"contract_revision"`
	Stateful           bool                       `json:"stateful"`
	StateTarget        string                     `json:"state_target,omitempty"`
	Environment        []string                   `json:"environment,omitempty"`
	RuntimeEnvironment map[string]string          `json:"runtime_environment,omitempty"`
	ProxyEnvironment   []string                   `json:"proxy_environment,omitempty"`
	PreflightFiles     map[string]json.RawMessage `json:"preflight_files,omitempty"`
	// PreflightEnvironment holds reviewed literal envs appended after the
	// credential placeholders in the unauthenticated tools/list probe, for
	// servers that exit without any configuration. The owner's submitted
	// secrets always win and this map never reaches the workload.
	PreflightEnvironment map[string]string `json:"preflight_environment,omitempty"`
	// ContextSources merges additional pinned repositories into the build
	// context — the reviewed equivalent of a Makefile vendoring step.
	ContextSources []ArtifactOverlay `json:"context_sources,omitempty"`
	// TCPForwards declares companion loopback forwards; each target_env must
	// be an env-delivered connection field carrying a host:port endpoint.
	TCPForwards      []TCPForward     `json:"tcp_forwards,omitempty"`
	Egress           []string         `json:"egress"`
	CredentialGroups [][]string       `json:"credential_groups,omitempty"`
	ReadTools        []string         `json:"read_tools"`
	ProbeTool        string           `json:"probe_tool,omitempty"`
	ProbeTools       []ProbeCandidate `json:"probe_tools,omitempty"`
	OAuth            *PreparedOAuth   `json:"oauth,omitempty"`
	Runbook          string           `json:"runbook"`
	Handoff          string           `json:"handoff"`
}

// ProbeCandidate names a reviewed read-only probe that applies only when its
// credential env var was actually delivered — for connectors whose credential
// groups are alternatives (Jira block or Confluence block, not both).
type ProbeCandidate struct {
	Tool string `json:"tool"`
	Env  string `json:"env"`
}

// PreparedOAuth is reviewed source-specific token delivery, not a guess from
// a tool's error text. The authorization-code exchange itself stays generic.
type PreparedOAuth struct {
	Provider     string   `json:"provider"`
	Scopes       []string `json:"scopes"`
	ClientInput  string   `json:"client_input"`
	TokenFile    string   `json:"token_file"`
	TokenAccount string   `json:"token_account"`
	TokenFormat  string   `json:"token_format"`
}

func PreparedCatalog() ([]PreparedEntry, error) {
	return parsePreparedCatalog(preparedCatalogJSON)
}

func parsePreparedCatalog(data []byte) ([]PreparedEntry, error) {
	var entries []PreparedEntry
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("%w: prepared catalog: %v", ErrInvalid, err)
	}
	if decoder.Decode(new(any)) != io.EOF || len(entries) > 128 {
		return nil, fmt.Errorf("%w: prepared catalog size or trailing data", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		_, sourceErr := entry.Source.ArchiveURL()
		if sourceErr != nil || !identity.ValidID(entry.ID) || seen[entry.ID] || seen[entry.Source.Repository] || entry.Name == "" || entry.License == "" || entry.Runbook == "" || entry.Handoff == "" || len(entry.Entrypoint) == 0 || len(entry.ReadTools) == 0 || entry.ProbeTool != "" && !slices.Contains(entry.ReadTools, entry.ProbeTool) || entry.ProbeTool != "" && len(entry.ProbeTools) > 0 {
			return nil, fmt.Errorf("%w: incomplete or duplicate prepared entry", ErrInvalid)
		}
		requiredFields := map[string]bool{}
		for _, field := range entry.Connection.Fields {
			requiredFields[field.Name] = field.Required
		}
		for _, candidate := range entry.ProbeTools {
			if !slices.Contains(entry.ReadTools, candidate.Tool) || !requiredFields[candidate.Env] {
				return nil, fmt.Errorf("%w: prepared probe candidate must be a reviewed read tool gated on a required field", ErrInvalid)
			}
		}
		for _, group := range entry.CredentialGroups {
			if len(group) < 2 {
				return nil, fmt.Errorf("%w: prepared credential group needs at least two members", ErrInvalid)
			}
			for _, name := range group {
				if !requiredFields[name] {
					return nil, fmt.Errorf("%w: prepared credential group member %q is not a required input", ErrInvalid, name)
				}
			}
		}
		if err := entry.Connection.Validate(); err != nil {
			return nil, err
		}
		brokerContract, err := entry.BrokerContract()
		if err != nil {
			return nil, err
		}
		if entry.Stateful != (entry.StateTarget != "") || entry.Stateful && !validContainerMountTarget(entry.StateTarget) {
			return nil, fmt.Errorf("%w: prepared state target", ErrInvalid)
		}
		if entry.OAuth != nil {
			a := entry.OAuth
			found := false
			for _, field := range entry.Connection.Fields {
				found = found || field.Name == a.ClientInput && field.Delivery == "file"
			}
			stateFile := false
			if brokerContract.State != nil && brokerContract.State.Target == entry.StateTarget {
				for _, file := range brokerContract.State.Files {
					stateFile = stateFile || file.Name == a.TokenFile && file.JSON
				}
			}
			if !entry.Stateful || !found || a.Provider != "google" || len(a.Scopes) != 1 || a.Scopes[0] != "https://www.googleapis.com/auth/calendar.readonly" || a.TokenFormat != "google-calendar-account-map" || a.TokenFile == "" || path.Base(a.TokenFile) != a.TokenFile || strings.ContainsAny(a.TokenFile, "\\\x00\r\n") || !stateFile || !identity.ValidID(a.TokenAccount) {
				return nil, fmt.Errorf("%w: prepared OAuth handoff", ErrInvalid)
			}
		}
		for name, body := range entry.PreflightFiles {
			if !credentialPattern.MatchString(name) || !json.Valid(body) || len(body) > 65536 {
				return nil, fmt.Errorf("%w: prepared preflight file", ErrInvalid)
			}
		}
		envFields := map[string]bool{}
		for _, field := range entry.Connection.Fields {
			if field.Delivery == "env" {
				envFields[field.Name] = true
			}
		}
		for _, forward := range entry.TCPForwards {
			if !envFields[forward.TargetEnv] {
				return nil, fmt.Errorf("%w: prepared tcp forward target is not an env connection field", ErrInvalid)
			}
		}
		if len(entry.ContextSources) > 4 {
			return nil, fmt.Errorf("%w: too many prepared context sources", ErrInvalid)
		}
		for _, overlay := range entry.ContextSources {
			if _, overlayErr := overlay.Source.ArchiveURL(); overlayErr != nil || overlay.Into == "" || !validGitHubSubfolder(overlay.Into) || overlay.Into == ".hub" || strings.HasPrefix(overlay.Into, ".hub/") {
				return nil, fmt.Errorf("%w: unsafe prepared context source", ErrInvalid)
			}
		}
		if len(entry.PreflightEnvironment) > 16 {
			return nil, fmt.Errorf("%w: prepared preflight environment", ErrInvalid)
		}
		for name, value := range entry.PreflightEnvironment {
			if !credentialPattern.MatchString(name) || secretName(name) || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
				return nil, fmt.Errorf("%w: prepared preflight environment", ErrInvalid)
			}
		}
		config := normalizeArtifactImportConfig(entry.apply(defaultSelfInstallConfig(entry.Source)))
		definition := ToolDefinition{Schema: SchemaVersion, DefinitionID: entry.ID, Version: config.Version, Transport: ContainerMCP,
			Source:      DefinitionSource{Image: config.Image, Digest: "sha256:" + string(bytes.Repeat([]byte("a"), 64)), ContextSources: append([]ArtifactOverlay(nil), entry.ContextSources...)},
			Credentials: config.Credentials, CredentialGroups: config.CredentialGroups, CredentialContractID: config.CredentialContractID, CredentialContractRevision: config.CredentialContractRevision, CredentialContractEnv: config.CredentialContractEnv,
			Environment: config.Environment, RuntimeEnvironment: config.RuntimeEnvironment, ProxyEnvironment: config.ProxyEnvironment, Tools: config.Tools, Workload: config.Workload, Execution: config.Execution, Health: config.Health}
		if err := definition.Validate(); err != nil {
			return nil, fmt.Errorf("prepared %s: %w", entry.ID, err)
		}
		seen[entry.ID], seen[entry.Source.Repository] = true, true
	}
	return entries, nil
}

func preparedForSource(source ArtifactSource) (PreparedEntry, bool, error) {
	entries, err := PreparedCatalog()
	for _, entry := range entries {
		if sameRepository(entry.Source.Repository, source.Repository) && entry.Source.CommitSHA == source.CommitSHA && entry.Source.Subfolder == source.Subfolder {
			return entry, true, err
		}
	}
	return PreparedEntry{}, false, err
}

// preparedForRepository finds the reviewed entry for an unpinned repository
// request. A bare URL means "this repository", not "whatever HEAD is today":
// the pinned commit is the only revision the overlay may cover, so it is the
// source an unpinned request selects.
func preparedForRepository(repository, subfolder string) (PreparedEntry, bool, error) {
	entries, err := PreparedCatalog()
	for _, entry := range entries {
		if sameRepository(entry.Source.Repository, repository) && entry.Source.Subfolder == subfolder {
			return entry, true, err
		}
	}
	return PreparedEntry{}, false, err
}

func (entry PreparedEntry) apply(config ArtifactImportConfig) ArtifactImportConfig {
	config.Language = entry.Language
	// The generator still infers the binary and upstream argv tail; the
	// reviewed argv may only extend that inferred prefix (read-only flags and
	// similar gates), never point the launch at a different binary.
	config.Entrypoint = append([]string(nil), entry.Entrypoint...)
	config.Credentials = entry.Connection.CredentialInputs()
	config.CredentialGroups = cloneStringGroups(entry.CredentialGroups)
	config.CredentialContractID, config.CredentialContractRevision = entry.ContractID, entry.ContractRevision
	config.CredentialContractEnv = map[string]string{}
	for _, field := range entry.Connection.Fields {
		if field.Delivery == "env" || field.Delivery == "file" {
			config.CredentialContractEnv[field.Name] = field.Name
		}
	}
	config.Environment = append([]string(nil), entry.Environment...)
	config.RuntimeEnvironment = cloneMap(entry.RuntimeEnvironment)
	config.ProxyEnvironment = append([]string(nil), entry.ProxyEnvironment...)
	config.ContextSources = append([]ArtifactOverlay(nil), entry.ContextSources...)
	config.Execution.Egress = append([]string(nil), entry.Egress...)
	config.Execution.TCPForwards = append([]TCPForward(nil), entry.TCPForwards...)
	config.Workload.Stateful = entry.Stateful
	return config
}

func (entry PreparedEntry) overlay(connection ConnectionRecipe) ConnectionRecipe {
	connection.Fields = slices.DeleteFunc(connection.Fields, func(field ConnectionField) bool { _, fixed := entry.RuntimeEnvironment[field.Name]; return fixed })
	for _, field := range entry.Connection.Fields {
		connection.Fields = slices.DeleteFunc(connection.Fields, func(existing ConnectionField) bool { return existing.Name == field.Name })
		connection.Fields = append(connection.Fields, field)
	}
	return normalizeConnection(connection)
}

func runtimeEnvironmentArgs(definition ToolDefinition) []string {
	var names, args []string
	for name := range definition.RuntimeEnvironment {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		args = append(args, "--env", name+"="+definition.RuntimeEnvironment[name])
	}
	return args
}

// proxyEnvironmentArgs fills the artifact's declared proxy env vars with the
// controller-owned egress URL. Review supplies only the names, so the value
// can never point at an unreviewed proxy.
func proxyEnvironmentArgs(definition ToolDefinition, proxyURL string) []string {
	var args []string
	for _, name := range slices.Sorted(slices.Values(definition.ProxyEnvironment)) {
		args = append(args, "--env", name+"="+proxyURL)
	}
	return args
}
