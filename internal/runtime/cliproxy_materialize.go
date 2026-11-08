package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passportview"
)

// cliproxyPlane is the credential-delivery seam the init companion uses: File
// returns one grant's mounted file payload, Env one env-delivered value. The
// real implementation acquires and releases a Broker lease per call; tests
// substitute a fake.
type cliproxyPlane interface {
	File(ctx context.Context, grant string) ([]byte, error)
	Env(ctx context.Context, grant, target string) (string, error)
}

type cliproxyMaterializeArgs struct {
	Instance, Owner                         string
	GeneratedDir, BaseDir, AuthsDir, CfgDir string
}

// brokerCliproxyPlane delivers through Credential Broker workload leases:
// each call acquires a lease, materializes, reads the payload and releases.
// The session and the key are granted to different workload slots — the
// broker admits one dedicated credential per (binding, context, principal),
// so the api key's init grant pins <instance>-key, not <instance>.
type brokerCliproxyPlane struct {
	cfg      credentialbroker.Config
	actor    identity.Envelope
	instance string
}

func (p brokerCliproxyPlane) File(ctx context.Context, grant string) ([]byte, error) {
	return p.cfg.ResolveFile(ctx, p.actor, p.instance, p.instance, grant)
}

func (p brokerCliproxyPlane) Env(ctx context.Context, grant, target string) (string, error) {
	key := p.instance + "-key"
	return p.cfg.ResolveEnv(ctx, p.actor, key, key, grant, target)
}

// runCliproxyMaterialize is the init-companion entrypoint: the compose plan
// names the instance in HUB_CLIPROXY_INSTANCE and mounts the generated
// passports, base config, session and rendered-config volumes at the fixed
// paths below (env-overridable for tests).
func runCliproxyMaterialize(ctx context.Context) error {
	instance := strings.TrimSpace(os.Getenv("HUB_CLIPROXY_INSTANCE"))
	if instance == "" {
		return errors.New("HUB_CLIPROXY_INSTANCE is required")
	}
	owner := strings.TrimSpace(os.Getenv("HUB_USER_ID"))
	if owner == "" {
		return errors.New("HUB_USER_ID is required")
	}
	cfg, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_CLIPROXY_")
	if err != nil {
		return err
	}
	if !cfg.Enabled() {
		return errors.New("HUB_CREDENTIAL_BROKER_CLIPROXY_ config incomplete")
	}
	actor := identity.Envelope{Schema: identity.Schema, PrincipalID: owner, ContextID: owner, RuntimeID: owner, ExternalIdentityID: owner, ConversationID: "cliproxy-materialize", DeliveryTargetID: "cliproxy-materialize", PolicyVersion: "cliproxy-materialize"}
	plane := brokerCliproxyPlane{cfg: cfg, actor: actor, instance: instance}
	return cliproxyMaterialize(ctx, plane, cliproxyMaterializeArgs{
		Instance: instance, Owner: owner,
		GeneratedDir: env("HUB_CLIPROXY_GEN_DIR", "/run/model-gen"),
		BaseDir:      env("HUB_CLIPROXY_BASE_DIR", "/cliproxy-base"),
		AuthsDir:     env("HUB_CLIPROXY_AUTHS_DIR", "/cliproxy/auths"),
		CfgDir:       env("HUB_CLIPROXY_CFG_DIR", "/run/cliproxy-cfg"),
	})
}

// modelGrantFile maps each models.yaml service's auth refs to the Broker
// grant ids the operator enrolled, split by acquiring consumer ("init" for
// the instance init companion, "relay" for model-relay) — grant ids are
// references, never secret material.
type modelGrantFile map[string]map[string]map[string]string

// cliproxyMaterialize delivers the service's session file and proxy_access
// key into the instance's tmpfs views (R7.2): /cliproxy/auths gets exactly
// the session file, /run/cliproxy-cfg gets the operator config plus its
// api-keys entry. Nothing touches the persistent bind.
func cliproxyMaterialize(ctx context.Context, plane cliproxyPlane, args cliproxyMaterializeArgs) error {
	eff, err := passportview.LoadEffectiveGenerated(args.GeneratedDir)
	if err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	service := ""
	for name, svc := range eff.Models.Services {
		if svc.Via != "cliproxy" {
			continue
		}
		u, uerr := url.Parse(svc.Endpoint)
		if uerr == nil && u.Hostname() == args.Instance {
			service = name
			break
		}
	}
	if service == "" {
		return fmt.Errorf("cliproxy materialize: no cliproxy service for instance %q", args.Instance)
	}
	var grantMap modelGrantFile
	raw, err := os.ReadFile(filepath.Join(args.GeneratedDir, "model-grants.json"))
	if err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	if err := json.Unmarshal(raw, &grantMap); err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	grants := grantMap[service]
	svc := eff.Models.Services[service]
	if svc.Auth.Upstream == nil || svc.Auth.ProxyAccess == nil {
		return fmt.Errorf("cliproxy materialize: service %s is not a cliproxy session service", service)
	}
	sessionGrant := grants[svc.Auth.Upstream.Ref]["init"]
	keyGrant := grants[svc.Auth.ProxyAccess.Ref]["init"]
	if sessionGrant == "" || keyGrant == "" {
		return fmt.Errorf("cliproxy materialize: service %s has no enrolled grants", service)
	}
	session, err := plane.File(ctx, sessionGrant)
	if err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	if err := os.MkdirAll(args.AuthsDir, 0700); err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	if err := replaceReadOnlyMaterial(filepath.Join(args.AuthsDir, "session.json"), session); err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	key, err := plane.Env(ctx, keyGrant, "MODEL_API_KEY")
	if err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	base, err := os.ReadFile(filepath.Join(args.BaseDir, "config.yaml"))
	if err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	var cfg yaml.Node
	if err := yaml.Unmarshal(base, &cfg); err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	if len(cfg.Content) == 0 || cfg.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("cliproxy materialize: base config is not a mapping document")
	}
	// The bind may still hold retired plaintext api-keys; the rendered config
	// carries only the Broker-materialized key, so stale entries are dropped.
	content := cfg.Content[0].Content[:0]
	for i := 0; i+1 < len(cfg.Content[0].Content); i += 2 {
		if cfg.Content[0].Content[i].Value == "api-keys" {
			continue
		}
		content = append(content, cfg.Content[0].Content[i], cfg.Content[0].Content[i+1])
	}
	cfg.Content[0].Content = append(content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "api-keys"},
		&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: key}}})
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	if err := os.MkdirAll(args.CfgDir, 0700); err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	if err := replaceReadOnlyMaterial(filepath.Join(args.CfgDir, "config.yaml"), out); err != nil {
		return fmt.Errorf("cliproxy materialize: %w", err)
	}
	return nil
}

func replaceReadOnlyMaterial(path string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".cliproxy-materialize-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Chmod(0400); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if old, statErr := os.Lstat(path); statErr == nil {
		if !old.Mode().IsRegular() {
			return errors.New("existing materialized file is not regular")
		}
		if err := os.Chmod(path, 0600); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		_ = os.Chmod(path, 0400)
		return err
	}
	return nil
}
