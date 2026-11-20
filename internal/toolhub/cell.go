package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Cell guest layout — shared with spawn-controller's cell spec builder
// (the moved v0 constants; the worker side lives in
// services/spawn-controller/internal/spawn/cells.go, CHG-0336 S6).
const (
	cellInitGuest    = "/cellinit"
	cellWorkGuest    = "/work"
	cellHomeGuest    = "/cellhome"
	cellUserTmpGuest = "/usertmp"
	cellToolsetGuest = "/tools"
	credProxyPort    = 3129
	cellProxyPort    = 3128
)

// cellWarmKey computes the reuse key a lifecycle tier implies; empty means
// ephemeral (fresh worker every call). The key embeds every trust boundary —
// principal, binding or job — so reuse can never cross callers.
func cellWarmKey(lifecycle, principal, bindingID, jobID, image string, toolset []string) string {
	switch lifecycle {
	case api.CLILifecycleTask:
		if jobID == "" {
			return ""
		}
		return lifecycle + "|" + principal + "|" + jobID + "|" + image
	case api.CLILifecycleBinding:
		key := lifecycle + "|" + principal + "|" + bindingID + "|" + image
		if len(toolset) > 0 {
			refs := append([]string(nil), toolset...)
			sort.Strings(refs)
			key += "|" + cellHash(strings.Join(refs, " "))
		}
		return key
	case api.CLILifecycleToolbox:
		refs := append([]string(nil), toolset...)
		sort.Strings(refs)
		return lifecycle + "|" + principal + "|" + cellHash(image, strings.Join(refs, " "))
	default:
		return ""
	}
}

func cellHash(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// cellImageRef resolves the cell image: owner artifact plans pin their own
// image+manifest digest; catalog commands run on the configured tools image.
// Toolbox cells always run the tools image as their Base: member artifacts
// mount read-only under /tools/<i>, and an ELF interpreter resolves against
// the cell rootfs — a scratch member base would leave dynamic binaries
// without a loader. Every returned ref is the daemon's image ID —
// create-by-ID so a retagged local name cannot swap content between
// resolution and create.
func (c *genericController) cellImageRef(ctx context.Context, req api.CliExecRequest) (string, error) {
	if req.Plan.Image != "" && req.Lifecycle != api.CLILifecycleToolbox {
		if !api.DigestPattern.MatchString(req.Plan.Digest) {
			return "", fmt.Errorf("%w: CLI image plan needs a manifest digest", api.ErrInvalid)
		}
		// image@manifest-digest resolves when the daemon knows the repo
		// digest (pulled/pushed or containerd-store loads); a locally loaded
		// quarantine image carries only the tag, so the name falls back to
		// it — the same resolution the container-MCP path performs.
		if resolved, err := c.resolveCellImage(ctx, req.Plan.Image+"@"+req.Plan.Digest); err == nil {
			return resolved, nil
		}
		return c.resolveCellImage(ctx, req.Plan.Image)
	}
	return c.resolveCellImage(ctx, c.config.CLI.ToolsImage)
}

// resolveCellImage maps any accepted cell image ref (digest-pinned ref, local
// name:tag, or image ID) to the daemon's immutable image ID. Missing images
// fail closed — a cell never runs an unresolved name.
func (c *genericController) resolveCellImage(ctx context.Context, ref string) (string, error) {
	out, err := c.docker(ctx, "image", "inspect", ref, "--format", "{{.Id}}")
	id := strings.TrimSpace(string(out))
	if err != nil || !api.DockerImageIDPattern.MatchString(id) {
		return "", fmt.Errorf("%w: cli cell image %q unavailable", api.ErrIsolation, ref)
	}
	return id, nil
}

// resolveToolbox returns the operator-pinned member images for a toolbox id.
// Each configured image@sha256 ref resolves to the daemon image ID the same
// way the cell image does — locally loaded artifacts carry only the tag.
func (c *genericController) resolveToolbox(ctx context.Context, id string) ([]string, error) {
	if id == "" {
		return nil, nil
	}
	members := c.config.CLI.Toolboxes[id]
	if len(members) == 0 {
		return nil, fmt.Errorf("%w: unknown cli toolbox %q", api.ErrUnauthorized, id)
	}
	c.mu.Lock()
	cached, ok := c.toolboxes[id]
	c.mu.Unlock()
	if ok {
		return append([]string(nil), cached...), nil
	}
	resolved := make([]string, 0, len(members))
	for _, ref := range members {
		image, err := c.resolveCellImage(ctx, ref)
		if err != nil {
			if at := strings.LastIndex(ref, "@"); at > 0 {
				image, err = c.resolveCellImage(ctx, ref[:at])
			}
		}
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, image)
	}
	c.mu.Lock()
	if c.toolboxes == nil {
		c.toolboxes = map[string][]string{}
	}
	c.toolboxes[id] = append([]string(nil), resolved...)
	c.mu.Unlock()
	return resolved, nil
}

// evictToolbox drops the cached member resolution for id; a failed cell
// claim can mean a resolved image ID was pruned between resolution and use.
func (c *genericController) evictToolbox(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	delete(c.toolboxes, id)
	c.mu.Unlock()
}

func (c *genericController) dockerRuntimes(ctx context.Context) (map[string]bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtimesOK {
		return c.runtimes, nil
	}
	body, err := c.docker(ctx, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return nil, fmt.Errorf("%w: docker runtime list: %v", api.ErrIsolation, err)
	}
	var runtimes map[string]json.RawMessage
	if json.Unmarshal(body, &runtimes) != nil {
		return nil, fmt.Errorf("%w: docker runtime list unparseable", api.ErrIsolation)
	}
	names := map[string]bool{}
	for name := range runtimes {
		names[name] = true
	}
	c.runtimes, c.runtimesOK = names, true
	return names, nil
}
