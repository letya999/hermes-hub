package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// This command is a host operation, never an agent-facing MCP method. Its input
// is an operator-reviewed record; --confirm is the explicit human act that
// stamps the reviewer's digest onto the record before the protected store
// checks revisions and pins.
func runCapability(args []string) error {
	f := flag.NewFlagSet("capability", flag.ContinueOnError)
	kind := f.String("kind", "", "policy, profile, group, agent-tools, preview, connectors, team, team-share or team-drop")
	teamID := f.String("team", "", "team id for team-share")
	definitionID := f.String("definition", "", "definition id for team-share")
	definitionVersion := f.String("version", "", "definition version for team-share, or the already-registered version agent-tools repins to")
	backend := f.String("backend", "", "credential backend for team-share")
	locator := f.String("locator", "", "credential locator for team-share")
	brokerContext := f.String("broker-context", "", "Broker context owning the shared credential (required for credential-broker team-share)")
	keys := f.String("keys", "", "comma-separated credential keys for team-share")
	connectionID := f.String("connection-id", "", "team connection id for team-drop")
	file := f.String("file", "", "operator-reviewed JSON record")
	issuer := f.String("issuer", "operator", "operator identity recorded as issuer and confirmer")
	confirm := f.Bool("confirm", false, "stamp this operator's confirmation onto the reviewed record")
	principal := f.String("principal", os.Getenv("HUB_PRINCIPAL_ID"), "principal owning the agent-tools binding")
	contextID := f.String("context", os.Getenv("HUB_CONTEXT_ID"), "context owning the agent-tools binding")
	runtimeID := f.String("runtime", os.Getenv("HUB_RUNTIME_ID"), "runtime owning the agent-tools binding")
	policyVersion := f.String("policy-version", os.Getenv("HUB_POLICY_VERSION"), "policy version stamped on the agent-tools binding")
	storePath := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "protected ToolHub registry path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (*kind != "policy" && *kind != "profile" && *kind != "group" && *kind != "agent-tools" && *kind != "preview" && *kind != "connectors" && *kind != "team" && *kind != "team-share" && *kind != "team-drop") {
		return errors.New("capability requires --kind policy|profile|group|agent-tools|preview|connectors|team|team-share|team-drop; no positional arguments")
	}
	if *kind == "connectors" {
		manifest := api.RecommendedConnectorManifest()
		if err := manifest.Validate(); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(manifest)
	}
	if (*kind == "team-share" || *kind == "team-drop") && *file != "" {
		return errors.New("team-share and team-drop take flags, not --file")
	}
	if *kind != "agent-tools" && *kind != "team-share" && *kind != "team-drop" && *file == "" {
		return errors.New("capability requires --file for policy, profile, group, preview and team")
	}
	if *kind == "group" {
		var group api.CapabilityGroup
		if err := readCapabilityRecord(*file, &group); err != nil {
			return err
		}
		if err := api.ValidateCapabilityGroup(group); err != nil {
			return err
		}
		canonical, err := json.Marshal(group)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "review digest: %s\n", api.ConfirmationDigest(group))
		_, err = os.Stdout.Write(append(canonical, '\n'))
		return err
	}
	if *storePath == "" {
		return errors.New("capability requires --toolhub-store")
	}
	absolute, err := filepath.Abs(*storePath)
	if err != nil {
		return err
	}
	store, err := loadOperatorStore(absolute)
	create := errors.Is(err, os.ErrNotExist)
	if err != nil && !create {
		return err
	}
	if create {
		store = toolhub.NewStore()
	}
	defer func() { _ = store.CloseDB() }()
	var id string
	var revision uint64
	var status api.Status
	if *kind == "preview" {
		// Read-only old-to-new diff on the same evaluator dispatch uses. No
		// store write, no confirmation gate — the operator previews the draft,
		// then runs --kind profile --confirm to apply exactly what was shown.
		var candidate api.CapabilityProfile
		if err := readCapabilityRecord(*file, &candidate); err != nil {
			return err
		}
		preview, err := store.PreviewCapabilityProfile(candidate)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(preview)
	}
	if *kind == "team-share" {
		if *teamID == "" || *definitionID == "" || *definitionVersion == "" || *backend == "" || *locator == "" {
			return errors.New("team-share requires --team, --definition, --version, --backend and --locator")
		}
		keyList := []string{}
		for _, key := range strings.Split(*keys, ",") {
			if key = strings.TrimSpace(key); key != "" {
				keyList = append(keyList, key)
			}
		}
		connection, _, err := store.ShareTeamConnection(*teamID, *definitionID, *definitionVersion, *backend, *locator, *brokerContext, keyList)
		if err != nil {
			return err
		}
		if create {
			if err := store.Save(absolute); err != nil {
				return err
			}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": *kind, "id": connection.ConnectionID})
	}
	if *kind == "team-drop" {
		if *connectionID == "" {
			return errors.New("team-drop requires --connection-id")
		}
		if err := store.DropTeamConnection(*connectionID); err != nil {
			return err
		}
		if create {
			if err := store.Save(absolute); err != nil {
				return err
			}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": *kind, "id": *connectionID})
	}
	if *kind == "agent-tools" {
		if *principal == "" || *contextID == "" || *runtimeID == "" || *policyVersion == "" {
			return errors.New("agent-tools requires --principal, --context, --runtime and --policy-version (or HUB_* equivalents)")
		}
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				_ = store.CloseDB()
				store, err = loadOperatorStore(absolute)
				if err != nil {
					return err
				}
			}
			// The built-in catalog is compiled into this binary and reviewed with
			// the release; binding carries only operator-supplied identity.
			definition := agenttools.HubToolsDefinition()
			if *definitionVersion != "" {
				stored, err := store.Definition(definition.DefinitionID, *definitionVersion)
				if err != nil {
					return err
				}
				definition = stored
			} else if err := store.RegisterDefinition(definition); err != nil {
				return err
			}
			auth := identity.Envelope{Schema: identity.Schema, PrincipalID: *principal, ExternalIdentityID: *principal,
				ContextID: *contextID, RuntimeID: *runtimeID, ConversationID: "operator", DeliveryTargetID: "operator", PolicyVersion: *policyVersion}
			binding, err := store.Enable(auth, definition.DefinitionID, definition.Version)
			if create && err == nil {
				err = store.Save(absolute)
			}
			if err == nil {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": *kind, "id": definition.DefinitionID, "revision": definition.Version, "binding": binding.ToolBindingID})
			}
			if create || !retryableStoreConflict(err) || attempt == 2 {
				return err
			}
		}
		return api.ErrConflict
	}
	if *kind == "policy" {
		var record api.CapabilityPolicy
		if err := readCapabilityRecord(*file, &record); err != nil {
			return err
		}
		if err := confirmRecord(*confirm, *issuer, &record); err != nil {
			return err
		}
		if err := store.PutCapabilityPolicy(record); err != nil {
			return err
		}
		id, revision, status = record.PolicyID, record.Revision, record.Status
	} else if *kind == "team" {
		var record api.Team
		if err := readCapabilityRecord(*file, &record); err != nil {
			return err
		}
		if err := confirmRecord(*confirm, *issuer, &record); err != nil {
			return err
		}
		if err := store.PutTeam(record); err != nil {
			return err
		}
		id, revision, status = record.TeamID, record.Revision, record.Status
	} else {
		var record api.CapabilityProfile
		if err := readCapabilityRecord(*file, &record); err != nil {
			return err
		}
		if err := confirmRecord(*confirm, *issuer, &record); err != nil {
			return err
		}
		if err := store.PutCapabilityProfile(record); err != nil {
			return err
		}
		id, revision, status = record.ProfileID, record.Revision, record.Status
	}
	if create {
		if err := store.Save(absolute); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": *kind, "id": id, "revision": revision, "status": status})
}

// confirmRecord is the human gate: without --confirm it prints the canonical
// review digest and refuses; with --confirm it stamps issuer+time+digest so the
// durable record binds the exact reviewed bytes to the operator identity.
func confirmRecord(confirmed bool, issuer string, record any) error {
	digest := api.ConfirmationDigest(record)
	if !confirmed {
		fmt.Fprintf(os.Stderr, "review digest: %s\nre-run with --issuer %s --confirm to commit\n", digest, issuer)
		return errors.New("record not confirmed")
	}
	if err := api.Confirm(record, issuer, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "confirmed digest: %s\n", digest)
	return nil
}

func writeAtomicMode(path string, body []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".workspace-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readCapabilityRecord(path string, record any) error {
	f, err := os.Open(path) // #nosec G304 -- explicitly supplied operator review file; output contains IDs only.
	if err != nil {
		return err
	}
	defer f.Close()
	const maxRecordBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxRecordBytes {
		return errors.New("capability record exceeds 4 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(record); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("exactly one capability JSON record required")
	}
	return nil
}
