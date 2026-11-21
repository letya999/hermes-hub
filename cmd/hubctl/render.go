package main

import (
	"context"
	"encoding/json"
	"fmt"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/locale"
	"os"
	"path/filepath"
)

// renderClient is the contract-read seam for the operator render path:
// production resolves through applySpawnClient; tests inject a fixture
// reader so the ToolHub-migration tail stays exercisable.
var renderClient = func(dir string) (spawnv1.EffectiveReader, error) {
	client, _, err := spawnClientFor(dir)
	return client, err
}

func renderEnvironment(ctx context.Context, dir, root, environment string) error {
	client, err := renderClient(dir)
	if err != nil {
		return err
	}
	if err := stack.RenderEnvironment(ctx, client, dir, root, environment); err != nil {
		return err
	}
	settings, err := stack.ReadEffective(ctx, client, dir, environment)
	if err != nil {
		return err
	}
	return migrateToolhubBindings(dir, settings)
}

// renderEnvironmentFor renders the effective bundle the caller already
// resolved — converge renders exactly the activated spec (S7d).
func renderEnvironmentFor(ctx context.Context, client spawnv1.EffectiveReader, dir, root, environment string, eff spawnv1.Effective) error {
	if err := stack.RenderEnvironmentFor(ctx, client, dir, root, environment, eff); err != nil {
		return err
	}
	settings, err := stack.SettingsForEffective(dir, eff, environment)
	if err != nil {
		return err
	}
	return migrateToolhubBindings(dir, settings)
}

// migrateToolhubBindings rolls this space's active ToolHub bindings and
// capability profiles onto the policy version this render computed
// (SPEC-0053 R1.2): a rights-hash change must not orphan grants the operator
// never revoked. The migration scans live state rather than trusting the
// previous manifest's `from` — envelopes are minted at the rendered hash, so
// entities pinned at a version no manifest recorded still brick the
// projection (the CHG-0230 live outage). Migration runs at render time so the
// deploy that follows already finds matching state.
func migrateToolhubBindings(dir string, s stack.Settings) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	manifest := stack.ExpectedToolManifest(s)
	// A managed space's ToolHub state lives in the infra sibling's runtime
	// dir — resolving only the caller's own root would miss the live store
	// (SPEC-0090 R5.1).
	storePath := api.ResolveStorePath(filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub"))
	if _, err := os.Stat(storePath); err != nil { // #nosec G703 -- store path resolves inside the operator's space dir argument.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	body, err := os.ReadFile(filepath.Join(dir, "generated", "tool-manifest."+s.Environment+".json")) // #nosec G703 -- dir is the operator's space argument and Environment is the validated dev/prod enum.
	var rendered stack.ToolManifest
	if err != nil || json.Unmarshal(body, &rendered) != nil || rendered.Schema != manifest.Schema ||
		rendered.Context != manifest.Context || rendered.PolicyVersion != manifest.PolicyVersion {
		return fmt.Errorf("%w: %s", api.ErrConflict, locale.T(locale.Operator(), "hubctl.toolhub-render-drift"))
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		return fmt.Errorf("%s: %w", locale.T(locale.Operator(), "hubctl.toolhub-store-unavailable"), err)
	}
	defer func() { _ = store.CloseDB() }()
	report, err := toolhub.MigrateProjection(store, s.User, manifest.Context, s.User, manifest.PolicyVersion)
	if err != nil {
		return fmt.Errorf("%s: %w", locale.T(locale.Operator(), "hubctl.toolhub-migration-failed", manifest.PolicyVersion), err)
	}
	if report.BindingsMoved > 0 {
		fmt.Fprintln(os.Stderr, locale.T(locale.Operator(), "hubctl.toolhub-bindings-migrated", report.BindingsMoved, manifest.PolicyVersion))
	}
	for _, id := range report.ProfilesBlocked {
		fmt.Fprintln(os.Stderr, locale.T(locale.Operator(), "hubctl.toolhub-profile-stale", id))
	}
	return nil
}

// effectiveSpec resolves the space's active effective bundle through the
// spawn contract — the only read path after S7d. applySpawnClient derives
// the tenant from the space dir the same way the read does.
func effectiveSpec(ctx context.Context, dir string) (spawnv1.Effective, error) {
	client, tenant, err := spawnClientFor(dir)
	if err != nil {
		return spawnv1.Effective{}, err
	}
	spec, err := client.Effective(ctx, tenant, filepath.Base(dir))
	if err != nil {
		return spawnv1.Effective{}, err
	}
	return spawnv1.Assemble(spec.Documents)
}

// readEffectiveSettings is ReadEffective with client resolution folded in —
// the shape every hubctl read site shares after S7d.
func readEffectiveSettings(ctx context.Context, dir, environment string) (stack.Settings, error) {
	client, _, err := spawnClientFor(dir)
	if err != nil {
		return stack.Settings{}, err
	}
	return stack.ReadEffective(ctx, client, dir, environment)
}
