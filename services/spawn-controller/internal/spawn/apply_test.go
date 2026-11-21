package spawn_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/spawn"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/store"
	"github.com/letya999/hermes-hub/shared/store/storetest"
	"gopkg.in/yaml.v3"
)

// The apply CAS refuses a base that is not the space's effective digest
// before anything writes: no revision activates, the digest stays.
func TestApplyStaleBaseRefused(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()
		base := effectiveDigest(t, revs)

		_, err := revs.Apply(ctx, tenant, "alice", map[string]map[string]map[string]any{
			"user": userDocs("UTC"),
		}, "sha256:"+strings.Repeat("0", 64), false)
		if err == nil || !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale base: %v", err)
		}
		if got := effectiveDigest(t, revs); got != base {
			t.Fatalf("stale apply moved the digest to %s", got)
		}
		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		active := 0
		for _, rev := range list {
			if rev.State == "active" {
				active++
			}
		}
		if active != 1 {
			t.Fatalf("stale apply left %d active user revisions", active)
		}
	})
}

// A dry-run compiles the would-be set — the receipt carries the new
// digest and the classified diff — but activates nothing: the effective
// digest and the active layer set do not move.
func TestApplyDryRunActivatesNothing(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()
		base := effectiveDigest(t, revs)

		out, err := revs.Apply(ctx, tenant, "alice", map[string]map[string]map[string]any{
			"user": userDocs("UTC"),
		}, base, true)
		if err != nil {
			t.Fatalf("dry-run apply: %v", err)
		}
		if out.EffectiveDigest == "" || out.EffectiveDigest == base {
			t.Fatalf("dry-run digest = %s, want a fresh compiled digest", out.EffectiveDigest)
		}
		if out.PreviousDigest != base || len(out.Changes) == 0 {
			t.Fatalf("dry-run receipt: previous=%s changes=%v", out.PreviousDigest, out.Changes)
		}
		if got := effectiveDigest(t, revs); got != base {
			t.Fatalf("dry-run moved the effective digest to %s", got)
		}
		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		active := 0
		for _, rev := range list {
			if rev.State == "active" {
				active++
			}
		}
		if active != 1 {
			t.Fatalf("dry-run activated: %d active user revisions", active)
		}
	})
}

// The confirmed apply moves the digest atomically; a failed downstream
// action re-activates the previous set anchored on the new digest — the
// rollback replays and the previous spec serves again (Decision 13).
func TestApplyFailedActionRollsBack(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()
		base := effectiveDigest(t, revs)

		out, err := revs.Apply(ctx, tenant, "alice", map[string]map[string]map[string]any{
			"user": userDocs("UTC"),
		}, base, false)
		if err != nil || out.EffectiveDigest == base {
			t.Fatalf("apply: %v digest=%s", err, out.EffectiveDigest)
		}
		if len(out.PreviousRevisions) == 0 {
			t.Fatal("apply did not report the previous set")
		}

		// The action plan fails mid-run: hubctl re-activates the previous
		// set with the CAS anchored on the digest it just committed.
		if _, err := revs.Activate(ctx, tenant, "alice", out.PreviousRevisions, &out.EffectiveDigest); err != nil {
			t.Fatalf("rollback activate: %v", err)
		}
		if got := effectiveDigest(t, revs); got != base {
			t.Fatalf("rollback digest = %s, want %s", got, base)
		}
		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var activeID string
		for _, rev := range list {
			if rev.State == "active" {
				activeID = rev.ID
			}
		}
		var rolled bool
		for _, prev := range out.PreviousRevisions {
			if prev == activeID {
				rolled = true
			}
		}
		if !rolled {
			t.Fatalf("rolled-back active %s is not in the previous set %v", activeID, out.PreviousRevisions)
		}
	})
}

// The settings edit writes the authored config back, publishes a new
// user revision and activates it on the digest CAS; the mirror's
// `current` pointer follows the new digest. Stale bases and missing
// config.edit_own refuse with localized contract errors.
func TestSettingsEditEndToEnd(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		root := t.TempDir()
		revs := testRevisions(t, url)
		revs.SpacesRoot = root
		ctx := context.Background()

		settingsUserDocs := userDocs("Europe/Berlin")
		settingsUserDocs["config"]["runtime"].(map[string]any)["compression"] = map[string]any{"enabled": false}
		settingsUserDocs["grants"]["allow"].(map[string]any)["config"] = []any{"edit_own"}

		if _, err := revs.CreateSpace(ctx, tenant, "alice", "dev"); err != nil {
			t.Fatalf("create space: %v", err)
		}
		layers := layerDocs()
		layers["user"] = settingsUserDocs
		// The org ceiling must admit the config capability before the user
		// layer may grant edit_own — ceilings intersect down the stack.
		layers["org-ceiling"]["grants"]["allow"] = append(layers["org-ceiling"]["grants"]["allow"].([]any), "config")
		ids := map[string]string{}
		for _, layer := range spawn.LayerOrder {
			docs, ok := layers[layer]
			if !ok {
				continue
			}
			rev, err := revs.Submit(ctx, tenant, "alice", layer, docs)
			if err != nil {
				t.Fatalf("submit %s: %v", layer, err)
			}
			ids[layer] = rev.ID
		}
		var all []string
		for _, layer := range spawn.LayerOrder {
			id, ok := ids[layer]
			if !ok {
				continue
			}
			rev, err := revs.Validate(ctx, tenant, "alice", id)
			if err != nil {
				t.Fatalf("validate %s: %v", layer, err)
			}
			if rev.State != "validated" {
				t.Fatalf("validate %s %s: state %s detail %s", layer, id, rev.State, rev.Detail)
			}
			all = append(all, id)
		}
		if _, err := revs.Activate(ctx, tenant, "alice", all, nil); err != nil {
			t.Fatalf("activate set: %v", err)
		}
		base := effectiveDigest(t, revs)

		// The authored passport tree mirrors the stored user layer — the
		// settings edit refuses to hide unpublished drift inside its commit.
		authoredDir := filepath.Join(root, "alice", "passports")
		for name, doc := range settingsUserDocs {
			body, err := yaml.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(authoredDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(authoredDir, name+".yaml"), body, 0600); err != nil {
				t.Fatal(err)
			}
		}

		got, err := revs.GetSettings(ctx, tenant, "alice", "compression.enabled")
		if err != nil {
			t.Fatalf("settings read: %v", err)
		}
		if got.PublishedRevision != base || got.PublishedValues["compression.enabled"] != false || got.SecretValuesIncluded {
			t.Fatalf("settings read: %+v", got)
		}

		stale := "sha256:" + strings.Repeat("0", 64)
		enabled := "true"
		if _, err := revs.EditSettings(ctx, tenant, "alice", "compression.enabled", &enabled, stale); err == nil || !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale base edit: %v", err)
		}

		out, err := revs.EditSettings(ctx, tenant, "alice", "compression.enabled", &enabled, base)
		if err != nil {
			t.Fatalf("settings edit: %v", err)
		}
		if !out.Changed || out.PublishedRevision == base {
			t.Fatalf("settings edit result: %+v", out)
		}
		if digest := effectiveDigest(t, revs); digest != out.PublishedRevision {
			t.Fatalf("effective digest = %s, edit reported %s", digest, out.PublishedRevision)
		}

		// The authored file carries the edit — the next `space apply`
		// submits the same content instead of reverting it.
		patched, err := os.ReadFile(filepath.Join(authoredDir, "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var patchedDoc map[string]any
		if err := yaml.Unmarshal(patched, &patchedDoc); err != nil {
			t.Fatal(err)
		}
		leaf := patchedDoc["runtime"].(map[string]any)["compression"].(map[string]any)["enabled"]
		if leaf != true {
			t.Fatalf("authored config compression.enabled = %v", leaf)
		}
		// S7d: the edit activated a new digest — the deleted file mirror
		// writes nothing under the spaces root.
		if _, err := os.Stat(filepath.Join(root, "alice", "generated", "passports")); !os.IsNotExist(err) {
			t.Fatalf("activation wrote a generated passport tree: %v", err)
		}

		// A space whose grants lack config.edit_own refuses the same edit
		// (denied) — the right must sit in the effective allow, not the file.
		if _, err := revs.CreateSpace(ctx, tenant, "dana", "dev"); err != nil {
			t.Fatalf("create dana: %v", err)
		}
		// Dana compiles a fresh layer set: no config ceiling (edit_own is
		// refused) and no published port — alice already reserved 6080.
		danaLayers := layerDocs()
		danaLayers["user"] = userDocs("Europe/Berlin")
		delete(danaLayers["platform-template"]["config"], "ports")
		danaIDs := map[string]string{}
		for _, layer := range spawn.LayerOrder {
			docs, ok := danaLayers[layer]
			if !ok {
				continue
			}
			rev, err := revs.Submit(ctx, tenant, "dana", layer, docs)
			if err != nil {
				t.Fatalf("dana submit %s: %v", layer, err)
			}
			danaIDs[layer] = rev.ID
		}
		var danaAll []string
		for _, layer := range spawn.LayerOrder {
			if id, ok := danaIDs[layer]; ok {
				rev, err := revs.Validate(ctx, tenant, "dana", id)
				if err != nil {
					t.Fatalf("dana validate %s: %v", layer, err)
				}
				if rev.State != "validated" {
					t.Fatalf("dana validate %s %s: state %s detail %s", layer, id, rev.State, rev.Detail)
				}
				danaAll = append(danaAll, id)
			}
		}
		if _, err := revs.Activate(ctx, tenant, "dana", danaAll, nil); err != nil {
			t.Fatalf("dana activate: %v", err)
		}
		danaDigest := effectiveDigestFor(t, revs, "dana")
		danaDir := filepath.Join(root, "dana", "passports")
		for name, doc := range userDocs("Europe/Berlin") {
			body, _ := yaml.Marshal(doc)
			if err := os.MkdirAll(danaDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(danaDir, name+".yaml"), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := revs.EditSettings(ctx, tenant, "dana", "compression.enabled", &enabled, danaDigest); err == nil || !errors.Is(err, httpapi.ErrForbidden) {
			t.Fatalf("edit without config.edit_own: %v", err)
		}
	})
}

func effectiveDigestFor(t *testing.T, revs *spawn.Revisions, space string) string {
	t.Helper()
	spec, err := revs.Effective(context.Background(), tenant, space)
	if err != nil {
		t.Fatalf("effective %s: %v", space, err)
	}
	return spec.Digest
}

// Activate itself carries the digest CAS inside its write transaction —
// the direct route (rollback, /v1/spaces/{space}/activations) never passes
// through Apply's pre-check, so the guard must hold here: a base that is
// not the space's effective digest refuses and nothing moves.
func TestActivateRefusesStaleBaseDirect(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()
		base := effectiveDigest(t, revs)
		stale := "sha256:" + strings.Repeat("0", 64)

		rev := submitValidated(t, revs, "user", userDocs("UTC"))
		if _, err := revs.Activate(ctx, tenant, "alice", []string{rev.ID}, &stale); err == nil || !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale base activate: %v", err)
		}
		if got := effectiveDigest(t, revs); got != base {
			t.Fatalf("stale activate moved the digest to %s", got)
		}
		assertRevisionState(t, revs, rev.ID, "validated")
	})
}

// assertRevisionState reads the revision's FSM state through the public
// list — there is no per-id getter.
func assertRevisionState(t *testing.T, revs *spawn.Revisions, id, want string) {
	t.Helper()
	list, err := revs.ListRevisions(context.Background(), tenant, "alice", "user")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rev := range list {
		if rev.ID == id {
			if rev.State != want {
				t.Fatalf("revision %s state = %s, want %s", id, rev.State, want)
			}
			return
		}
	}
	t.Fatalf("revision %s missing", id)
}

// Two writers activating against the same base digest on one space have
// exactly one winner: the CAS is inside the write transaction (immediate
// txlock serializes writers), so the loser reads the moved pointer and
// conflicts — the active set can never be a torn merge of both.
func TestActivateConcurrentBaseRace(t *testing.T) {
	storetest.ForEach(t, func(t *testing.T, url string) {
		revs := testRevisions(t, url)
		setupSpace(t, revs)
		ctx := context.Background()
		base := effectiveDigest(t, revs)

		first := submitValidated(t, revs, "user", userDocs("UTC"))
		second := submitValidated(t, revs, "user", userDocs("Asia/Almaty"))
		type outcome struct {
			id  string
			err error
		}
		results := make(chan outcome, 2)
		for _, id := range []string{first.ID, second.ID} {
			go func(id string) {
				_, err := revs.Activate(ctx, tenant, "alice", []string{id}, &base)
				results <- outcome{id, err}
			}(id)
		}
		var winner, loser string
		for range 2 {
			out := <-results
			switch {
			case out.err == nil:
				winner = out.id
			case errors.Is(out.err, store.ErrConflict):
				loser = out.id
			default:
				t.Fatalf("unexpected activate error: %v", out.err)
			}
		}
		if winner == "" || loser == "" {
			t.Fatalf("race needs exactly one winner and one conflicted loser: winner=%q loser=%q", winner, loser)
		}
		if got := effectiveDigest(t, revs); got == base {
			t.Fatal("a winner committed but the digest did not move")
		}
		list, err := revs.ListRevisions(ctx, tenant, "alice", "user")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var active []string
		for _, rev := range list {
			if rev.State == "active" {
				active = append(active, rev.ID)
			}
		}
		if len(active) != 1 || active[0] != winner {
			t.Fatalf("race left active user revisions %v — want exactly the winner %s", active, winner)
		}
		assertRevisionState(t, revs, loser, "validated")
	})
}
