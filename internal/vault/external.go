package vault

import (
	"fmt"
	"strings"
)

// externalExcludes are the vault-root zones that never enter history: hub
// runtime sidecars, OK's own state and the derived graph. They are covered
// by .okignore for the editor and by this filter for git — .gitignore in a
// pre-existing vault may be empty, so the sweep cannot rely on it.
var externalExcludes = []string{".git/", ".graph/", ".ok/", "runtime/"}

// CommitExternalEdits sweeps uncommitted working-tree changes into one
// commit authored hermes-owner (SPEC-0085 R8.4): the only external writer
// the vault can see is the owner — the OpenKnowledge editor or direct file
// edits — so their provenance enters history before a governed write could
// absorb the same bytes under a system author. Returns the swept path
// count; a clean tree sweeps nothing and creates no commit.
func CommitExternalEdits(root string) (int, error) {
	status := gitCmd(root, "status", "--porcelain", "--untracked-files=all")
	out, err := status.Output()
	if err != nil {
		return 0, fmt.Errorf("vault external sweep: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.Trim(line[3:], `"`)
		if !excluded(p) {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return 0, nil
	}
	for _, args := range [][]string{
		append([]string{"add", "-A", "--"}, paths...),
		{"-c", "user.name=hermes-owner", "-c", "user.email=owner@vault.local", "commit", "--quiet", "-m", "vault: owner external edits"},
	} {
		cmd := gitCmd(root, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return 0, fmt.Errorf("vault external sweep git %s: %w (%s)", args[0], err, out)
		}
	}
	return len(paths), nil
}

// excluded reports whether a vault-relative path lives in a never-committed
// sidecar zone (compared on the slash form, root-anchored).
func excluded(rel string) bool {
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "./")
	for _, zone := range externalExcludes {
		if rel == strings.TrimSuffix(zone, "/") || strings.HasPrefix(rel, zone) {
			return true
		}
	}
	return false
}
