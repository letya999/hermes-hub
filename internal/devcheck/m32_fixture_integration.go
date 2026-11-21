//go:build integration

package devcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// M32Fixture keeps authored configuration separate from operator-issued rights
// and the derived registry. No running deployment is read or modified here.
type M32Fixture struct {
	Root, Infra, StorePath string
	GroupSpace             string
	Users                  []string
	Auth                   map[string]identity.Envelope
}

func NewM32Fixture(root, environment, providerURL, suffix string) (M32Fixture, error) {
	return newM32Fixture(root, environment, providerURL, suffix, false)
}

// NewM32GroupFixture replaces the second private home with a dedicated group
// home; memory stays selected/enabled to exercise the exact-run gate itself.
func NewM32GroupFixture(root, environment, providerURL, suffix string) (M32Fixture, error) {
	return newM32Fixture(root, environment, providerURL, suffix, true)
}

func newM32Fixture(root, environment, providerURL, suffix string, groupSecond bool) (M32Fixture, error) {
	f := M32Fixture{Root: root, Users: []string{"m32a" + suffix, "m32b" + suffix}, Auth: map[string]identity.Envelope{}}
	if !filepath.IsAbs(root) || environment != "prod" && environment != "dev" ||
		!identity.ValidID(f.Users[0]) || !identity.ValidID(f.Users[1]) {
		return f, fmt.Errorf("invalid M32 fixture location or identity")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return f, fmt.Errorf("M32 fixture requires a regular isolated directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		return f, fmt.Errorf("M32 fixture requires an empty isolated directory")
	}
	f.Infra = filepath.Join(root, "spaces", f.Users[0])
	f.StorePath = filepath.Join(f.Infra, "runtime", "toolhub", "store.json")
	docs, err := catalog.Generate()
	if err != nil {
		return f, err
	}
	now := time.Now().UTC()
	allow := map[string][]string{
		"memory": {"search", "read", "remember", "edit", "dreaming"},
		"models": {"use", "configure"}, "tools": {"list", "use", "configure"},
		"tasks": {"schedule", "delegate", "observe"}, "config": {"read", "edit_own"},
		"goals": {"read", "edit", "remove"}, "context": {"read", "edit"},
		"stack": {"change"}, "workspace": {"read", "write"},
		"agents":   {"inspect_self", "monitor_self", "restart_self"},
		"channels": {"send"},
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		return f, err
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("# M32 isolated acceptance\n"), 0644); err != nil {
		return f, err
	}
	rights := []api.RightRule{}
	for capability, operations := range allow {
		for _, operation := range operations {
			rights = append(rights, api.RightRule{Capability: capability, Operation: operation})
		}
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(f.Infra, "runtime", "toolhub", "governance.json"))
	for i, user := range f.Users {
		dir := filepath.Join(root, "spaces", user)
		if err := stack.InitEnvironment(dir, user, environment); err != nil {
			return f, err
		}
		zone := "UTC"
		if i == 1 {
			zone = "America/New_York"
		}
		kind := "user"
		if groupSecond && i == 1 {
			kind = "group"
			f.GroupSpace = user
		}
		services := "  hermes-runtime: {mode: supervised}\n  model-relay: {mode: per-space}\n  cliproxy: {mode: shared}\n  toolhub-relay: {mode: per-space}\n  egress-relay: {mode: per-space}\n  toolhub: {mode: shared}\n  supervisor: {mode: shared}\n  communication-hub: {mode: shared}\n  credential-broker: {mode: shared}\n"
		bodies := map[string]string{
			"stack":     fmt.Sprintf("identity: {user: %s, kind: %s, infra: %t}\nservices:\n%s", user, kind, i == 0, services),
			"config":    fmt.Sprintf("runtime: {timezone: %s}\nmemory: {enabled: true, nightly_rollup: \"02:30\"}\nchannels:\n  telegram: {bot: {ref: fixture-bot}}\n", zone),
			"models":    fmt.Sprintf("default: fixture\nroutes: {fixture: {ladder: [fixture]}}\nservices:\n  fixture: {via: openai-compatible, endpoint: %q, allow_private: true, models: [{id: m32-fixture}]}\n", providerURL),
			"workspace": "writable: [/workspace]\nread_only: [/config, /skills]\n",
			"tools":     fmt.Sprintf("binding: {profile_id: %s-default, generation: 1}\ncore:\n  memory: {via: toolhub}\n  assignments: {via: toolhub}\n  context: {via: toolhub}\nextended:\n  files: {via: toolhub}\n  routines: {via: toolhub}\n  watches: {via: toolhub}\n  goals: {via: toolhub}\n", user),
		}
		grants := "allow:\n"
		for capability, operations := range allow {
			grants += "  " + capability + ": ["
			for n, operation := range operations {
				if n != 0 {
					grants += ", "
				}
				grants += operation
			}
			grants += "]\n"
		}
		bodies["grants"] = grants
		for _, name := range passport.Names {
			if err := os.WriteFile(filepath.Join(dir, "passports", name+".yaml"), []byte(bodies[name]), 0600); err != nil {
				return f, err
			}
		}
		for capability, operations := range allow {
			for _, operation := range operations {
				grant := api.NewRightGrant(api.DeterministicID("grant", user, capability, operation), user, capability, operation, "", "Isolated M32 acceptance", "operator", now.Add(time.Hour))
				if err := api.Confirm(&grant, "operator", now); err != nil {
					return f, err
				}
				if err := gov.GrantRight(grant, docs.Rights, now); err != nil {
					return f, err
				}
			}
		}
		revision, err := passport.CompileAuthored(dir, environment, now, docs, stack.ReservedSiblingPorts(dir), allow)
		if err != nil {
			return f, fmt.Errorf("compile M32 authored passports: %w", err)
		}
		if err := publishGenerated(dir, revision); err != nil {
			return f, err
		}
		if err := stack.RenderEnvironment(context.Background(), generatedReader{dir}, dir, root, environment); err != nil {
			return f, err
		}
		settings, err := stack.ReadEffective(context.Background(), generatedReader{dir}, dir, environment)
		if err != nil {
			return f, err
		}
		auth := identity.TelegramEnvelope(user, int64(11+i), user, stack.PolicyVersion(settings))
		auth.CapabilityProfile, auth.Environment, auth.Generation, auth.Organization = user+"-default", environment, 1, settings.Organization
		f.Auth[user] = auth
	}
	store := toolhub.NewStore()
	definition := catalog.HubToolsDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		return f, err
	}
	store.RightsDoc, store.RightsLedger = docs.Rights, gov.Load
	ceiling := []api.CapabilityRule{}
	for _, tool := range definition.Tools {
		if tool.Audience != api.AudienceModel {
			continue
		}
		for _, use := range tool.Uses {
			ceiling = append(ceiling, api.CapabilityRule{CapabilityID: tool.CapabilityID, ImplementationDigest: api.DefinitionDigest(definition), Action: use.Action, Resource: use.Resource,
				Limits: api.CapabilityLimits{OutputBytes: definition.Execution.OutputBytes, TimeoutSeconds: definition.Execution.TimeoutSeconds}})
		}
	}
	for _, user := range f.Users {
		auth := f.Auth[user]
		policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: user + "-policy", Organization: auth.Organization, Members: []string{user}, Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: "Isolated M32 acceptance", Status: api.ActiveStatus, RightCeiling: rights, Ceiling: ceiling, Defaults: ceiling}
		if err := api.Confirm(&policy, "operator", now); err != nil {
			return f, err
		}
		if err := store.PutCapabilityPolicy(policy); err != nil {
			return f, err
		}
		current, err := generatedRevisionID(filepath.Join(root, "spaces", user))
		if err != nil {
			return f, err
		}
		profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: user, ContextID: user, RuntimeID: user, Environment: environment, Generation: 1, PolicyVersion: auth.PolicyVersion, PassportRevision: current, PolicyID: policy.PolicyID, PolicyRevision: 1, Revision: 1, IssuedBy: "operator", IssuedAt: now, Reason: policy.Reason, Status: api.ActiveStatus, Rights: rights}
		if err := api.Confirm(&profile, "operator", now); err != nil {
			return f, err
		}
		if err := store.PutCapabilityProfile(profile); err != nil {
			return f, err
		}
		binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: user, ContextID: user, RuntimeID: user, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		binding.ToolBindingID = api.DeterministicBindingID(user, user, user, definition.DefinitionID, definition.Version, "", "")
		if err := store.PutBinding(binding); err != nil {
			return f, err
		}
	}
	if err := store.Save(f.StorePath); err != nil {
		return f, err
	}
	for _, user := range f.Users {
		dir := filepath.Join(root, "spaces", user)
		current, err := generatedRevisionID(dir)
		if err != nil {
			return f, err
		}
		revision := passport.Revision{ID: current, Docs: map[string][]byte{}}
		for _, name := range passport.Names {
			revision.Docs[name], err = os.ReadFile(filepath.Join(dir, "generated", "passports", current, name+".yaml"))
			if err != nil {
				return f, err
			}
		}
		if err := migration.DeriveSpaceRights(dir, user, environment, revision, docs, now); err != nil {
			return f, err
		}
	}
	if err := stack.EnrollSiblingRuntimeTokens(context.Background(), generatedReader{f.Infra}, f.Infra, environment); err != nil {
		return f, err
	}
	return f, nil
}
