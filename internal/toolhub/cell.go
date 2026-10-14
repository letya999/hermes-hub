package toolhub

// Controller-owned CLI sandbox cells. A bounded-cli call no longer execs
// inside the ToolHub container; the controller creates a sibling container —
// the cell — that carries exactly one workspace bind, the read-only cellinit
// helper, no host state, no broker material and no docker socket. Warm tiers
// reuse cells behind a re-inspect + canary guard; anything that drifts is
// killed and audited, never reused.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cliWorkspace is the single bind a cell may carry. Scope none mounts
// nothing (the cell gets a scratch tmpfs at /work); binding binds the
// controller-local path the ToolHub resolved through OpenWorkloadWorkspace;
// principal binds <spaces_root>/<principal>/workspace resolved here — or
// <spaces_root>/<principal>/managed/<environment>/workspace when a managed
// deployment tier is configured and that directory exists.
type cliWorkspace struct {
	Scope  string `json:"scope"`
	Path   string `json:"path,omitempty"`
	Access string `json:"access,omitempty"`
}

// cliBrokeredCred is one credential delivered through the cell-local
// cred-proxy sidecar: the cell env carries only
// http://<proxy>/<host> and the proxy injects the real header on the
// reviewed upstream. The value crosses to the controller in the request
// body — over the bearer-authenticated control channel — but never enters
// container config, argv or logs.
type cliBrokeredCred struct {
	EnvName string `json:"env_name"`
	Host    string `json:"host"`
	Prefix  string `json:"prefix,omitempty"`
	Value   string `json:"value"`
}

// cliExecRequest is the /cli-exec contract. Plan carries the immutable
// definition identity and execution policy the same way /admit does.
type cliExecRequest struct {
	Plan            controllerPlan    `json:"plan"`
	Command         string            `json:"command"`
	Args            []string          `json:"args,omitempty"`
	Env             []string          `json:"env,omitempty"`
	Principal       string            `json:"principal_id"`
	ContextID       string            `json:"context_id"`
	BindingID       string            `json:"binding_id"`
	JobID           string            `json:"job_id,omitempty"`
	Lifecycle       string            `json:"lifecycle,omitempty"`
	ToolboxID       string            `json:"toolbox_id,omitempty"`
	Brokered        []cliBrokeredCred `json:"brokered,omitempty"`
	Stateless       bool              `json:"stateless,omitempty"`
	InstallPackages bool              `json:"install_packages,omitempty"`
	Workspace       cliWorkspace      `json:"workspace,omitempty"`
}

type cliExecResponse struct {
	Receipt  AdmissionReceipt `json:"receipt"`
	Output   string           `json:"output"`
	ExitCode int              `json:"exit_code"`
	// Error carries a completed-but-flagged outcome the caller maps to a
	// typed error — "output-limit" today. Denials stay on HTTP errors.
	Error string `json:"error,omitempty"`
}

// cliReleaseRequest kills warm cells selected by principal/binding/job.
type cliReleaseRequest struct {
	PrincipalID string `json:"principal_id"`
	BindingID   string `json:"binding_id,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// cliCell is one live sandbox container the controller owns.
type cliCell struct {
	name            string
	key             string // warm reuse key; "" for ephemeral and pool cells
	principal       string
	bindingID       string
	jobID           string
	lifecycle       string
	image           string // image@digest ref the cell was created from
	runtime         string // OCI runtime the create used ("" = daemon default runc)
	workHost        string // host path bound at /work; "" when tmpfs scratch
	workRW          bool
	installPackages bool
	network         string   // internal egress network name, "" = none
	sidecars        []string // companion container names (squid, cred-proxy)
	volumes         []string
	toolset         []string // toolbox image mounts
	canary          string
	created         time.Time
	lastUse         time.Time
	uses            int
	execSlot        chan struct{} // cap-1: one exec per cell at a time
}

// cellMetrics are the pool/lifecycle counters the acceptance criteria name;
// every mutating event also writes a controller audit log line.
type cellMetrics struct {
	creates     int64
	reuses      int64
	canaryFails int64
	rotations   int64
	poolHits    int64
	poolMisses  int64
	poolClaims  int64
	// claimWaitNanos accumulates the wall time pool callers spent waiting
	// for a cell; poolExhaustions counts bounded-wait rejections.
	claimWaitNanos  int64
	poolExhaustions int64
}

type cellRegistry struct {
	cells map[string]*cliCell // warm key -> cell
	pool  map[string][]*cliCell
	// claimedN reserves live-cell slots per image under lock so the pool cap
	// bounds idle+claimed cells; claimed maps busy cells by name so shutdown
	// reaps them too.
	claimedN map[string]int
	claimed  map[string]*cliCell
	metrics  cellMetrics
}

func newCellRegistry() *cellRegistry {
	return &cellRegistry{cells: map[string]*cliCell{}, pool: map[string][]*cliCell{}, claimedN: map[string]int{}, claimed: map[string]*cliCell{}}
}

const (
	cellInitGuest    = "/cellinit"
	cellWorkGuest    = "/work"
	cellHomeGuest    = "/cellhome"
	cellUserTmpGuest = "/usertmp"
	cellCanaryPath   = "/tmp/.hermes-canary"
	cellCanaryName   = ".hermes-canary"
	cellToolsetGuest = "/tools"
	credProxyPort    = 3129
	cellProxyPort    = 3128
)

func (c *genericController) cells() *cellRegistry {
	if c.registry == nil {
		c.registry = newCellRegistry()
	}
	return c.registry
}

// cellWarmKey computes the reuse key a lifecycle tier implies; empty means
// ephemeral (fresh cell every call). The key embeds every trust boundary —
// principal, binding or job — so reuse can never cross callers.
func cellWarmKey(lifecycle, principal, bindingID, jobID, image string, toolset []string) string {
	switch lifecycle {
	case CLILifecycleTask:
		if jobID == "" {
			return ""
		}
		return lifecycle + "|" + principal + "|" + jobID + "|" + image
	case CLILifecycleBinding:
		key := lifecycle + "|" + principal + "|" + bindingID + "|" + image
		if len(toolset) > 0 {
			refs := append([]string(nil), toolset...)
			sort.Strings(refs)
			key += "|" + cellHash(strings.Join(refs, " "))
		}
		return key
	case CLILifecycleToolbox:
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
// Toolbox cells always run the tools image as their base: member artifacts
// mount read-only under /tools/<i>, and an ELF interpreter resolves against
// the cell rootfs — a scratch member base would leave dynamic binaries
// without a loader. Every returned ref is the daemon's image ID —
// create-by-ID so a retagged local name cannot swap content between
// resolution and create.
func (c *genericController) cellImageRef(ctx context.Context, req cliExecRequest) (string, error) {
	if req.Plan.Image != "" && req.Lifecycle != CLILifecycleToolbox {
		if !digestPattern.MatchString(req.Plan.Digest) {
			return "", fmt.Errorf("%w: CLI image plan needs a manifest digest", ErrInvalid)
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
	if err != nil || !dockerImageIDPattern.MatchString(id) {
		return "", fmt.Errorf("%w: cli cell image %q unavailable", ErrIsolation, ref)
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
		return nil, fmt.Errorf("%w: unknown cli toolbox %q", ErrUnauthorized, id)
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
// create can mean a resolved image ID was pruned between resolution and use.
func (c *genericController) evictToolbox(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	delete(c.toolboxes, id)
	c.mu.Unlock()
}

// realEgress strips the loopback-only marker hosts; a plan whose egress is
// only loopback runs with --network none.
func realEgress(egress []string) []string {
	out := make([]string, 0, len(egress))
	for _, host := range egress {
		h := strings.ToLower(strings.TrimSpace(host))
		if h == "" || h == "127.0.0.1" || h == "localhost" || h == "::1" {
			continue
		}
		out = append(out, host)
	}
	return out
}

// cellUID picks the cell user: 10001 by default, or a stable per-principal
// uid (20000+) when the operator opted into uid separation — files a rw cell
// writes then carry a principal-derived uid on the host.
func (c *genericController) cellUID(req cliExecRequest, rw bool) string {
	if c.config.CLI.PerPrincipalUID && rw {
		h := fnv.New32a()
		h.Write([]byte(req.Principal))
		uid := 20000 + int(h.Sum32()%30000)
		return strconv.Itoa(uid) + ":" + strconv.Itoa(uid)
	}
	return "10001:10001"
}

// runtimeFlag maps the configured tier onto the docker --runtime flag; empty
// means runc (daemon default). runsc/kata must be listed by the daemon.
func (c *genericController) runtimeFlag(ctx context.Context) (string, error) {
	runtime := c.config.CLI.Runtime
	switch runtime {
	case "", "runc":
		return "", nil
	case "runsc", "kata":
	default:
		return "", fmt.Errorf("%w: unknown cli runtime %q", ErrInvalid, runtime)
	}
	runtimes, err := c.dockerRuntimes(ctx)
	if err != nil {
		return "", err
	}
	if !runtimes[runtime] {
		return "", fmt.Errorf("%w: cli runtime %q is not installed on the daemon", ErrIsolation, runtime)
	}
	return runtime, nil
}

func (c *genericController) dockerRuntimes(ctx context.Context) (map[string]bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtimesOK {
		return c.runtimes, nil
	}
	body, err := c.docker(ctx, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return nil, fmt.Errorf("%w: docker runtime list: %v", ErrIsolation, err)
	}
	var runtimes map[string]json.RawMessage
	if json.Unmarshal(body, &runtimes) != nil {
		return nil, fmt.Errorf("%w: docker runtime list unparseable", ErrIsolation)
	}
	names := map[string]bool{}
	for name := range runtimes {
		names[name] = true
	}
	c.runtimes, c.runtimesOK = names, true
	return names, nil
}

// validCellImageRef accepts the three refs a cell image may take: an
// image@sha256 digest pin, a local image ID, or a local name[:tag] the
// daemon resolves to an image ID at create time. Toolbox members stay
// digest-pinned only — caller-controlled surfaces never take a bare tag.
func validCellImageRef(ref string) error {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		if at == 0 || !digestPattern.MatchString(ref[at+1:]) || strings.ContainsAny(ref[:at], " \t\r\n") || strings.Contains(ref, "..") {
			return fmt.Errorf("%w: cell image %q must be digest-pinned", ErrInvalid, ref)
		}
		return nil
	}
	if dockerImageIDPattern.MatchString(ref) {
		return nil
	}
	if err := validateLocalImageName(ref); err != nil {
		return fmt.Errorf("%w: cell image %q", ErrInvalid, ref)
	}
	return nil
}

// validPinnedImageRef is the caller-visible form: toolbox members and
// artifact plans must pin an image@sha256 manifest digest; daemon-local
// tags would let the approved surface drift silently.
func validPinnedImageRef(ref string) error {
	at := strings.LastIndex(ref, "@")
	if at <= 0 || !digestPattern.MatchString(ref[at+1:]) || strings.ContainsAny(ref[:at], " \t\r\n") || strings.Contains(ref, "..") {
		return fmt.Errorf("%w: cell image %q must be digest-pinned", ErrInvalid, ref)
	}
	return nil
}
