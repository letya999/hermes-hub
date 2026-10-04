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
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// This command is a host operation, never an agent-facing MCP method. Its input
// is an operator-reviewed record; --confirm is the explicit human act that
// stamps the reviewer's digest onto the record before the protected store
// checks revisions and pins.
func runCapability(args []string) error {
	f := flag.NewFlagSet("capability", flag.ContinueOnError)
	kind := f.String("kind", "", "policy, profile, group, agent-tools, preview, connectors or native")
	file := f.String("file", "", "operator-reviewed JSON record")
	issuer := f.String("issuer", "operator", "operator identity recorded as issuer and confirmer")
	confirm := f.Bool("confirm", false, "stamp this operator's confirmation onto the reviewed record")
	settingsPath := f.String("settings", "", "space settings.yaml the native carve-out applies to")
	allow := f.String("allow", "", "comma-separated native toolsets to grant (empty revokes all)")
	principal := f.String("principal", os.Getenv("HUB_PRINCIPAL_ID"), "principal owning the agent-tools binding")
	contextID := f.String("context", os.Getenv("HUB_CONTEXT_ID"), "context owning the agent-tools binding")
	runtimeID := f.String("runtime", os.Getenv("HUB_RUNTIME_ID"), "runtime owning the agent-tools binding")
	policyVersion := f.String("policy-version", os.Getenv("HUB_POLICY_VERSION"), "policy version stamped on the agent-tools binding")
	storePath := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "protected ToolHub registry path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (*kind != "policy" && *kind != "profile" && *kind != "group" && *kind != "agent-tools" && *kind != "preview" && *kind != "connectors" && *kind != "native") {
		return errors.New("capability requires --kind policy|profile|group|agent-tools|preview|connectors|native; no positional arguments")
	}
	if *kind == "native" {
		return runCapabilityNative(*settingsPath, *allow, *confirm)
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

// runCapabilityNative grants or revokes the operator-level native-toolset
// carve-out for one managed runtime. Without --settings it prints the
// reviewed carve-out set; with --settings it surgically rewrites the
// `native_toolsets:` line in the space settings.yaml, re-validates the whole
// file through the same parser a spawn reads, and swaps it atomically.
// The grant renders into agent.disabled_toolsets: native tools bypass ToolHub
// admission entirely, so revocation takes effect on the next spawn.
func runCapabilityNative(settingsPath, allow string, confirm bool) error {
	if settingsPath == "" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"kind": "native", "carveout_toolsets": stack.NativeCarveoutToolsets(),
			"note": "add native_toolsets to a managed space settings.yaml, then re-render",
		})
	}
	var granted []string
	for _, name := range strings.Split(allow, ",") {
		if name = strings.TrimSpace(name); name != "" {
			granted = append(granted, name)
		}
	}
	if err := stack.ValidateNativeToolsets(granted); err != nil {
		return err
	}
	current, err := stack.Read(settingsPath)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	slices.Sort(granted)
	if slices.Equal(current.NativeToolsets, granted) {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": "native", "native_toolsets": granted, "changed": false})
	}
	if !confirm {
		fmt.Fprintf(os.Stderr, "native_toolsets: %s -> %s\nre-run with --confirm to commit\n",
			strings.Join(current.NativeToolsets, ","), strings.Join(granted, ","))
		return errors.New("carve-out not confirmed")
	}
	if err := writeNativeToolsets(settingsPath, granted); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"kind": "native", "native_toolsets": granted, "changed": true,
		"note": "restart or re-spawn the runtime; native tools have no per-call admission",
	})
}

var (
	nativeHeaderLine = regexp.MustCompile(`^native_toolsets\s*:`)
	nativeBlockEntry = regexp.MustCompile(`^\s+-\s`)
)

// writeNativeToolsets replaces the top-level `native_toolsets:` key (scalar,
// inline or block list) in a settings.yaml, preserving every other line, then
// proves the result still parses as valid managed settings before swapping.
func writeNativeToolsets(path string, names []string) error {
	body, err := os.ReadFile(path) // #nosec G304 -- operator-supplied settings path.
	if err != nil {
		return err
	}
	rendered := "native_toolsets: [" + strings.Join(names, ", ") + "]"
	if len(names) == 0 {
		rendered = "native_toolsets: []"
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	out := make([]string, 0, len(lines)+1)
	replaced := false
	for i := 0; i < len(lines); i++ {
		if !replaced && nativeHeaderLine.MatchString(lines[i]) {
			out = append(out, rendered)
			replaced = true
			for i+1 < len(lines) && nativeBlockEntry.MatchString(lines[i+1]) {
				i++
			}
			continue
		}
		out = append(out, lines[i])
	}
	if !replaced {
		out = append(out, rendered)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.WriteString(strings.Join(out, "\n") + "\n"); err != nil {
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
