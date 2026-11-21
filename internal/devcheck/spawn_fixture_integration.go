//go:build integration

package devcheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/passport"
	"gopkg.in/yaml.v3"
)

// spawnFixture is a real spawn-controller subprocess answering the v1 spawn
// contract over loopback HTTP with the docker backend. The devcheck gates
// that claim or read managed runtimes need the actual contract authority:
// effective passports and runtime claims are spawn-controller reads now, not
// on-disk generated trees.
type spawnFixture struct {
	URL    string
	Token  string
	Client spawnv1.Client
	stop   func()
}

var spawnBinary struct {
	once sync.Once
	path string
	err  error
}

func spawnControllerBinary(repoRoot string) (string, error) {
	spawnBinary.once.Do(func() {
		out := filepath.Join(os.TempDir(), "hermes-devcheck-spawn-controller"+binarySuffix())
		cmd := exec.Command("go", "build", "-o", out, "./services/spawn-controller")
		cmd.Dir = repoRoot
		if b, err := cmd.CombinedOutput(); err != nil {
			spawnBinary.err = fmt.Errorf("build spawn-controller: %w: %s", err, b)
			return
		}
		spawnBinary.path = out
	})
	return spawnBinary.path, spawnBinary.err
}

func binarySuffix() string {
	if filepath.Separator == '\\' {
		return ".exe"
	}
	return ""
}

// startSpawnController launches spawn-controller with the docker backend for
// the given platform root (spaces live at root/spaces) and waits for healthz.
func startSpawnController(ctx context.Context, repoRoot, platformRoot, image string) (*spawnFixture, error) {
	bin, err := spawnControllerBinary(repoRoot)
	if err != nil {
		return nil, err
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	listen := probe.Addr().String()
	_ = probe.Close()
	data, err := os.MkdirTemp("", "spawn-fixture-")
	if err != nil {
		return nil, err
	}
	token := "spawn-fixture-" + randomHex(8)
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		"HUB_BACKEND=docker",
		"HUB_TRUSTED=true",
		"HUB_LISTEN="+listen,
		"HUB_DATA_DIR="+data,
		"HUB_SERVICE_TOKEN="+token,
		"HUB_DATABASE_URL=sqlite://"+filepath.ToSlash(filepath.Join(data, "spawn.db")),
		"HUB_RUNTIME_IMAGE="+image,
		"HUB_SPACES_ROOT="+filepath.Join(platformRoot, "spaces"),
		"HUB_PLATFORM_ROOT="+platformRoot,
		// The controller runs host-native like its callers: no
		// HUB_DOCKER_HOST_ROOT, so claims return the relay's published
		// loopback port and mount sources stat as the host paths the
		// fixture wrote.
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	fx := &spawnFixture{
		URL:    "http://" + listen,
		Token:  token,
		Client: spawnv1.NewHTTPClient("http://"+listen, token, nil),
	}
	fx.stop = func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		_ = os.RemoveAll(data)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(fx.URL + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return fx, nil
			}
		}
		if time.Now().After(deadline) {
			fx.stop()
			return nil, fmt.Errorf("spawn-controller never answered healthz at %s", fx.URL)
		}
		select {
		case <-ctx.Done():
			fx.stop()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// spawnOwnedContainers lists the spawn-owned docker IDs of one space —
// workload and control relay both carry the managed/space labels the docker
// backend stamps on every container it launches.
func spawnOwnedContainers(ctx context.Context, space string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq",
		"--filter", "label=hermes-hub.managed=spawn-controller",
		"--filter", "label=hermes-hub.space="+space).Output()
	if err != nil {
		return nil, fmt.Errorf("list spawn-owned containers: %w", err)
	}
	var ids []string
	for _, id := range strings.Fields(string(out)) {
		ids = append(ids, id)
	}
	return ids, nil
}

// SpawnOwnedContainerID resolves the workload container name of a claimed
// runtime: the relay shares the space label, so pick the one whose name
// lacks the -ctl suffix. The control relay is <name>-ctl.
func SpawnOwnedContainerID(ctx context.Context, space string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq",
		"--filter", "label=hermes-hub.managed=spawn-controller",
		"--filter", "label=hermes-hub.space="+space,
		"--format", "{{.ID}} {{.Names}}").Output()
	if err != nil {
		return "", fmt.Errorf("resolve spawn-owned container: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && !strings.HasSuffix(fields[1], "-ctl") {
			return fields[1], nil
		}
	}
	return "", errors.New("spawn-owned workload container unavailable")
}

// RemoveSpawnOwned force-removes every spawn-owned container of the space.
func RemoveSpawnOwned(ctx context.Context, space string) {
	ids, err := spawnOwnedContainers(ctx, space)
	if err != nil {
		return
	}
	for _, id := range ids {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
	}
}

// publishAuthoredPassports submits the fixture's authored passports/*.yaml
// through the spawn contract as the space's user layer and activates it, so
// Effective reads answer the same compiled passport the contract would.
// Only the user layer is submitted: template layers may not carry grants,
// and with nothing above it the user layer's own grants map folds into the
// merged capability list and trusted ledger — the same effective documents
// CompileAuthored produced for the retired file mirror.
func publishAuthoredPassports(ctx context.Context, client spawnv1.Client, tenant spawnv1.Tenant, space, dir string) error {
	docNames := []string{"stack", "config", "models", "tools", "workspace", "grants"}
	docs := map[string]map[string]any{}
	for _, name := range docNames {
		raw, err := os.ReadFile(filepath.Join(dir, "passports", name+".yaml"))
		if err != nil {
			return fmt.Errorf("authored passport %s: %w", name, err)
		}
		var body map[string]any
		if err := yaml.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("authored passport %s: %w", name, err)
		}
		docs[name] = body
	}
	rev, err := client.SubmitRevision(ctx, space, "user", spawnv1.SubmitRevisionRequest{
		Org: tenant.Org, Owner: tenant.Owner, Documents: docs,
		IdempotencyKey: "devcheck-sub-" + space + "-user",
	})
	if err != nil {
		return fmt.Errorf("submit user: %w", err)
	}
	rev, err = client.ValidateRevision(ctx, space, rev.ID, spawnv1.RevisionRequest{
		Org: tenant.Org, Owner: tenant.Owner, IdempotencyKey: "devcheck-val-" + space + "-user",
	})
	if err != nil {
		return fmt.Errorf("validate user: %w", err)
	}
	if rev.State != "validated" {
		return fmt.Errorf("validate user %s: state %s detail %s", rev.ID, rev.State, rev.Detail)
	}
	if _, err := client.ActivateRevisions(ctx, space, spawnv1.ActivationRequest{
		Org: tenant.Org, Owner: tenant.Owner, Revisions: []string{rev.ID},
		IdempotencyKey: "devcheck-act-" + space,
	}); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	return nil
}

// SpawnEffectiveRevision reads the space's active bundle through the spawn
// contract and hands back the passport.Revision the retired generated tree
// mirrored: the compiled revision id (the value /config/revision serves)
// plus the six stored documents.
func SpawnEffectiveRevision(ctx context.Context, client spawnv1.Client, tenant spawnv1.Tenant, space string) (passport.Revision, error) {
	spec, err := client.Effective(ctx, tenant, space)
	if err != nil {
		return passport.Revision{}, fmt.Errorf("effective %s: %w", space, err)
	}
	eff, err := spawnv1.Assemble(spec.Documents)
	if err != nil {
		return passport.Revision{}, fmt.Errorf("effective %s: %w", space, err)
	}
	docs := make(map[string][]byte, len(spec.Documents))
	for name, raw := range spec.Documents {
		docs[name] = append([]byte(nil), raw...)
	}
	return passport.Revision{ID: eff.Header.Metadata.Revision, Docs: docs}, nil
}

// devcheckRepoRoot locates the repository root from the current working
// directory — cmd/devcheck runs from the checkout root while go test runs
// with the package directory as cwd.
func devcheckRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repository root (go.mod) not found from " + dir)
		}
		dir = parent
	}
}
