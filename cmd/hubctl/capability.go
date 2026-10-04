package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"gopkg.in/yaml.v3"
)

// This command is a host operation, never an agent-facing MCP method. Its input
// is an operator-reviewed record; --confirm is the explicit human act that
// stamps the reviewer's digest onto the record before the protected store
// checks revisions and pins.
func runCapability(args []string) error {
	f := flag.NewFlagSet("capability", flag.ContinueOnError)
	kind := f.String("kind", "", "policy, profile, group, agent-tools, preview, connectors or tools")
	file := f.String("file", "", "operator-reviewed JSON record")
	issuer := f.String("issuer", "operator", "operator identity recorded as issuer and confirmer")
	confirm := f.Bool("confirm", false, "stamp this operator's confirmation onto the reviewed record")
	settingsPath := f.String("settings", "", "space settings.yaml the tools plan applies to")
	set := f.String("set", "", "comma-separated name=backend tool entries (empty backend removes the entry)")
	principal := f.String("principal", os.Getenv("HUB_PRINCIPAL_ID"), "principal owning the agent-tools binding")
	contextID := f.String("context", os.Getenv("HUB_CONTEXT_ID"), "context owning the agent-tools binding")
	runtimeID := f.String("runtime", os.Getenv("HUB_RUNTIME_ID"), "runtime owning the agent-tools binding")
	policyVersion := f.String("policy-version", os.Getenv("HUB_POLICY_VERSION"), "policy version stamped on the agent-tools binding")
	storePath := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "protected ToolHub registry path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (*kind != "policy" && *kind != "profile" && *kind != "group" && *kind != "agent-tools" && *kind != "preview" && *kind != "connectors" && *kind != "tools") {
		return errors.New("capability requires --kind policy|profile|group|agent-tools|preview|connectors|tools; no positional arguments")
	}
	if *kind == "tools" {
		return runCapabilityTools(*settingsPath, *set, *confirm)
	}
	if *kind == "connectors" {
		manifest := toolhub.RecommendedConnectorManifest()
		if err := manifest.Validate(); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(manifest)
	}
	if *kind != "agent-tools" && *file == "" {
		return errors.New("capability requires --file for policy, profile, group and preview")
	}
	if *kind == "group" {
		var group toolhub.CapabilityGroup
		if err := readCapabilityRecord(*file, &group); err != nil {
			return err
		}
		if err := toolhub.ValidateCapabilityGroup(group); err != nil {
			return err
		}
		canonical, err := json.Marshal(group)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "review digest: %s\n", toolhub.ConfirmationDigest(group))
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
	store, err := toolhub.Load(absolute)
	create := errors.Is(err, os.ErrNotExist)
	if err != nil && !create {
		return err
	}
	if create {
		store = toolhub.NewStore()
	}
	var id string
	var revision uint64
	var status toolhub.Status
	if *kind == "preview" {
		// Read-only old-to-new diff on the same evaluator dispatch uses. No
		// store write, no confirmation gate — the operator previews the draft,
		// then runs --kind profile --confirm to apply exactly what was shown.
		var candidate toolhub.CapabilityProfile
		if err := readCapabilityRecord(*file, &candidate); err != nil {
			return err
		}
		preview, err := store.PreviewCapabilityProfile(candidate)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(preview)
	}
	if *kind == "agent-tools" {
		if *principal == "" || *contextID == "" || *runtimeID == "" || *policyVersion == "" {
			return errors.New("agent-tools requires --principal, --context, --runtime and --policy-version (or HUB_* equivalents)")
		}
		// The built-in catalog is compiled into this binary and reviewed with
		// the release; installing it is still an explicit operator act on the
		// protected store, and the binding carries only operator-supplied
		// identity (never model arguments).
		definition := agenttools.HubToolsDefinition()
		if err := store.RegisterDefinition(definition); err != nil {
			return err
		}
		binding := toolhub.ToolBinding{Schema: toolhub.SchemaVersion, PrincipalID: *principal, ContextID: *contextID, RuntimeID: *runtimeID,
			DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: *policyVersion,
			WorkloadClass: definition.Workload.Class, Status: toolhub.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		binding.ToolBindingID = toolhub.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
		if err := store.PutBinding(binding); err != nil {
			return err
		}
		if err := store.Save(absolute); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": *kind, "id": definition.DefinitionID, "revision": definition.Version, "binding": binding.ToolBindingID})
	}
	if *kind == "policy" {
		var record toolhub.CapabilityPolicy
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
	} else {
		var record toolhub.CapabilityProfile
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
	digest := toolhub.ConfirmationDigest(record)
	if !confirmed {
		fmt.Fprintf(os.Stderr, "review digest: %s\nre-run with --issuer %s --confirm to commit\n", digest, issuer)
		return errors.New("record not confirmed")
	}
	if err := toolhub.Confirm(record, issuer, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "confirmed digest: %s\n", digest)
	return nil
}

// runCapabilityTools inspects or edits the unified `tools:` surface of one
// space settings.yaml. Without --settings it prints the reviewed vocabulary;
// without --set it prints the compiled plan (which backend serves what);
// with --set name=backend it surgically rewrites the tools map, re-validates
// the whole file through the spawn parser and swaps atomically.
func runCapabilityTools(settingsPath, set string, confirm bool) error {
	if settingsPath == "" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"kind":              "tools",
			"backends":          []string{"native", "toolhub", "mcp[:server]", "off"},
			"carveout_toolsets": stack.NativeCarveoutToolsets(),
			"note":              "hubctl capability --kind tools --settings <space>/settings.yaml [--set name=backend,...] [--confirm]",
		})
	}
	current, err := stack.Read(settingsPath)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if strings.TrimSpace(set) == "" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": "tools", "plan": current.ToolPlan()})
	}
	updates, removals, err := parseToolSet(set)
	if err != nil {
		return err
	}
	candidate := current
	candidate.Tools = map[string]stack.ToolEntry{}
	for name, entry := range current.Tools {
		candidate.Tools[name] = entry
	}
	for _, name := range removals {
		delete(candidate.Tools, name)
	}
	for name, entry := range updates {
		candidate.Tools[name] = entry
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("tools plan would become invalid: %w", err)
	}
	before, after := current.ToolPlan(), candidate.ToolPlan()
	if !confirm {
		fmt.Fprintf(os.Stderr, "tools plan before: %+v\ntools plan after:  %+v\nre-run with --confirm to commit\n", before, after)
		return errors.New("tools plan not confirmed")
	}
	if err := writeToolEntries(settingsPath, updates, removals); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"kind": "tools", "plan": after, "changed": true,
		"note": "restart or re-spawn the runtime; native tools have no per-call admission",
	})
}

// parseToolSet parses `name=backend,name=backend` — `backend` is
// native|toolhub|mcp[:server]|off; an empty backend removes the entry.
func parseToolSet(set string) (map[string]stack.ToolEntry, []string, error) {
	updates := map[string]stack.ToolEntry{}
	var removals []string
	for _, pair := range strings.Split(set, ",") {
		name, backend, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found {
			return nil, nil, fmt.Errorf("invalid --set pair %q (want name=backend)", pair)
		}
		if name = strings.TrimSpace(name); name == "" {
			return nil, nil, fmt.Errorf("invalid --set pair %q: empty name", pair)
		}
		backend = strings.TrimSpace(backend)
		if backend == "" {
			removals = append(removals, name)
			continue
		}
		entry := stack.ToolEntry{Via: backend}
		if strings.HasPrefix(backend, "mcp:") {
			entry.Via, entry.Server = "mcp", strings.TrimPrefix(backend, "mcp:")
		}
		updates[name] = entry
	}
	return updates, removals, nil
}

// toolEntryNode renders an entry as the compact scalar form when possible,
// keeping written settings.yaml as terse as the operator's own style.
func toolEntryNode(entry stack.ToolEntry) (*yaml.Node, error) {
	if len(entry.Only) == 0 && len(entry.Except) == 0 && len(entry.Tools) == 0 && len(entry.Paths) == 0 && len(entry.Limits) == 0 {
		value := entry.Via
		if entry.Via == "mcp" && entry.Server != "" {
			value = "mcp:" + entry.Server
		}
		if entry.Server == "" || entry.Via == "mcp" {
			return &yaml.Node{Kind: yaml.ScalarNode, Value: value}, nil
		}
	}
	body, err := yaml.Marshal(entry)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}

// writeToolEntries upserts/removes keys inside the top-level `tools:` mapping
// of a settings.yaml, preserving comments and every other key, then proves
// the result still parses as valid settings before swapping atomically.
func writeToolEntries(path string, updates map[string]stack.ToolEntry, removals []string) error {
	body, err := os.ReadFile(path) // #nosec G304 -- operator-supplied settings path.
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("settings.yaml is not a YAML mapping")
	}
	root := doc.Content[0]
	var tools *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "tools" {
			tools = root.Content[i+1]
		}
	}
	if tools == nil {
		tools = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "tools"}, tools)
	}
	if tools.Kind != yaml.MappingNode {
		return errors.New("settings.yaml tools: is not a mapping")
	}
	setPair := func(name string, value *yaml.Node) {
		for i := 0; i+1 < len(tools.Content); i += 2 {
			if tools.Content[i].Value == name {
				if value == nil {
					tools.Content = append(tools.Content[:i], tools.Content[i+2:]...)
				} else {
					tools.Content[i+1] = value
				}
				return
			}
		}
		if value != nil {
			tools.Content = append(tools.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: name}, value)
		}
	}
	for _, name := range removals {
		setPair(name, nil)
	}
	names := make([]string, 0, len(updates))
	for name := range updates {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		node, err := toolEntryNode(updates[name])
		if err != nil {
			return err
		}
		setPair(name, node)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// The post-write file must pass the same strict parse a spawn performs;
	// a settings file that no longer validates never reaches the runtime.
	if _, err = stack.Read(name); err != nil {
		return fmt.Errorf("settings would become invalid: %w", err)
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
