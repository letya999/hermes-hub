package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/passportview"
)

// PlanMount is one host bind in the runtime container's filesystem contract
// (SPEC-0092 R5.2): the workspace passport — not a hardcoded list — decides
// what the agent sees and where it may write.
type PlanMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// MountPlan projects the workspace passport into container binds: the
// published /config view, the /workspace base with read-only sub-binds for
// every read_only subtree inside it, the /vault skeleton split by mode,
// the effective skills view and every declared /shared/* mount resolved
// through the same scope and credential checks legacy mounts used. wsBase
// is the workspace/vault parent — the space dir for unmanaged runtimes,
// managed/<env> for managed ones.
func MountPlan(dir, env, wsBase string, s Settings, ws passportview.Workspace) []PlanMount {
	plan := []PlanMount{
		{Source: filepath.Join(dir, "generated", "config-view", env), Target: "/config", ReadOnly: true},
		{Source: filepath.Join(wsBase, "workspace"), Target: "/workspace"},
	}
	// Read-only subtrees under /workspace ride nested binds over the rw base;
	// the longest read_only prefix wins inside the mount, matching file
	// admission's resolution.
	ro := slices.Clone(ws.ReadOnly)
	slices.Sort(ro)
	for _, p := range ro {
		if strings.HasPrefix(p, "/workspace/") {
			plan = append(plan, PlanMount{Source: filepath.Join(wsBase, filepath.FromSlash(p)), Target: p, ReadOnly: true})
		}
	}
	if !s.Has("memory_vault") {
		// The skeleton only applies to the legacy vault shape: under
		// memory_vault the whole dir/vault tree bind-mounts read-write —
		// these sub-mounts would shadow it with an empty managed tree and
		// RO-block the governed writer (SPEC-0085 R1.1).
		plan = append(plan,
			PlanMount{Source: filepath.Join(wsBase, "vault", "memory"), Target: "/vault/memory", ReadOnly: true},
			PlanMount{Source: filepath.Join(wsBase, "vault", "library"), Target: "/vault/library"},
			PlanMount{Source: filepath.Join(wsBase, "vault", "attachments"), Target: "/vault/attachments"},
		)
	}
	plan = append(plan, PlanMount{Source: GeneratedEffectiveSkillsDir(dir), Target: "/skills", ReadOnly: true})
	targets := make([]string, 0, len(ws.Mounts))
	for t := range ws.Mounts {
		targets = append(targets, t)
	}
	slices.Sort(targets)
	for _, t := range targets {
		m := ws.Mounts[t]
		src, readOnly, err := s.resolveMount(Mount{From: m.Source, To: t, Mode: m.Mode})
		if err != nil {
			continue // compile already rejected; render stays total
		}
		plan = append(plan, PlanMount{Source: src, Target: t, ReadOnly: readOnly})
	}
	return plan
}

// MaterializeWorkspaceLayout creates every host directory the workspace
// passport's boundary and the MountPlan's fixed skeleton need under wsBase.
// It creates directories only — never memory content — and never descends
// into existing trees such as workspace/projects (SPEC-0092 R5.2).
func MaterializeWorkspaceLayout(wsBase string, ws passportview.Workspace) error {
	names := []string{"workspace", "vault/memory", "vault/library", "vault/attachments"}
	for _, p := range slices.Concat(ws.Writable, ws.ReadOnly) {
		if strings.HasPrefix(p, "/workspace/") || strings.HasPrefix(p, "/vault/") {
			names = append(names, strings.TrimPrefix(p, "/"))
		}
	}
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(wsBase, filepath.FromSlash(name)), 0700); err != nil {
			return err
		}
	}
	return nil
}
