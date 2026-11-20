package main

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/locale"
	"os"
	"path/filepath"
)

func renderEnvironment(dir, root, environment string) error {
	if err := stack.RenderEnvironment(dir, root, environment); err != nil {
		return err
	}
	settings, err := stack.ReadEffective(dir, environment)
	if err != nil {
		return err
	}
	return migrateToolhubBindings(dir, settings)
}
func renderRevisionEnvironment(dir, root, environment, revision string) error {
	if err := stack.RenderRevisionEnvironment(dir, root, environment, revision); err != nil {
		return err
	}
	settings, err := stack.ReadEffectiveRevision(dir, environment, revision)
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
	manifest := stack.ExpectedToolManifest(s)
	storePath := api.ResolveStorePath(filepath.Join(dir, "runtime", "toolhub"))
	if _, err := os.Stat(storePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	body, err := os.ReadFile(filepath.Join(dir, "generated", "tool-manifest."+s.Environment+".json"))
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
