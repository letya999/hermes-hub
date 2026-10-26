package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gofrs/flock"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/exposure"
	"github.com/letya999/hermes-hub/internal/identity"
)

type Deployment struct {
	PolicyVersion string `json:"policy_version,omitempty"`
	ContextID     string `json:"context_id,omitempty"`
	Config
	Slots               []string `json:"slots"`
	ExposureConfig      string   `json:"exposure_config"`
	ControlTokenFile    string   `json:"control_token_file"`
	ControllerURL       string   `json:"controller_url,omitempty"`
	ControllerTokenFile string   `json:"controller_token_file,omitempty"`
	DataListen          string   `json:"data_listen"`
	ControlListen       string   `json:"control_listen"`
}

func LoadDeployment(p string) (Deployment, error) {
	var cfg Deployment
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return cfg, ErrInvalid
	}
	f, err := os.Open(p)
	if err != nil {
		return cfg, ErrUnavailable
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(new(any)) != io.EOF || cfg.Config.validate() != nil || len(cfg.Slots) < 1 || len(cfg.Slots) > cfg.MaxSites || !path.IsAbs(cfg.ExposureConfig) || !path.IsAbs(cfg.ControlTokenFile) || cfg.DataListen != "127.0.0.1:8181" || cfg.ControlListen != "0.0.0.0:8182" {
		return cfg, ErrInvalid
	}
	seen := map[string]bool{}
	for _, name := range cfg.Slots {
		if !cfg.validName(name) || seen[name] {
			return cfg, ErrInvalid
		}
		seen[name] = true
	}
	cfg.Config.Slots = append([]string{}, cfg.Slots...)
	return cfg, nil
}
func Run(ctx context.Context, cfg Deployment) error {
	if runtime.GOOS != "linux" {
		return ErrUnavailable
	}
	releaseZone, err := acquireZoneLease("/zone/address.lock")
	if err != nil {
		return err
	}
	defer releaseZone()
	token, err := readToken(cfg.ControlTokenFile)
	if err != nil {
		return err
	}
	ec, err := exposure.Load(cfg.ExposureConfig)
	if err != nil || ec.Provider != "sish" || ec.Zone != cfg.Owner || ec.Relay.Domain != cfg.Domain || len(ec.Endpoints)+len(cfg.Slots) > ec.Max() {
		return ErrDenied
	}
	allowed := map[string]bool{}
	for _, n := range cfg.Slots {
		if _, ok := ec.Endpoints[n]; ok {
			return ErrConflict
		}
		allowed[n] = true
	}
	broker, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_RUNTIME_")
	if err != nil {
		return ErrUnavailable
	}
	keyroot, err := os.OpenRoot(filepath.Dir(cfg.ExposureConfig))
	if err != nil {
		return ErrUnavailable
	}
	provider := exposure.NewSishProvider(ec, exposure.ProviderOptions{MaxConnections: 64, ConnectionTimeout: 30 * time.Second, Root: keyroot, Broker: broker, Auth: cfg.brokerAuth()})
	defer provider.Shutdown()
	routes := &siteRoutes{provider: provider, cfg: ec, allowed: allowed, live: map[string]routeLease{}}
	defer routes.close()
	var executor Executor
	if cfg.ControllerURL != "" {
		ct, err := readToken(cfg.ControllerTokenFile)
		if err != nil {
			return err
		}
		executor = &ControllerClient{URL: cfg.ControllerURL, Token: ct, HTTP: &http.Client{Timeout: 28 * time.Second}}
	}
	service, err := Open(cfg.Config, Options{Routes: routes, Executor: executor})
	if err != nil {
		return err
	}
	defer service.Close()
	// Both listeners exist before any URL may be returned through the control API.
	data, err := net.Listen("tcp", cfg.DataListen)
	if err != nil {
		return ErrUnavailable
	}
	defer data.Close()
	control, err := net.Listen("tcp", cfg.ControlListen)
	if err != nil {
		return ErrUnavailable
	}
	defer control.Close()
	return serveListeners(ctx, service, token, data, control)
}

func serveListeners(ctx context.Context, service *Service, token string, data, control net.Listener) error {
	data = limitConnections(data, 128)
	control = limitConnections(control, 16)
	server := func(h http.Handler) *http.Server {
		return &http.Server{ErrorLog: log.New(io.Discard, "", 0), Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	}
	ds, cs := server(service), server(service.ControlHandler(token))
	failures := make(chan error, 2)
	go func() { failures <- ds.Serve(data) }()
	go func() { failures <- cs.Serve(control) }()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = cs.Shutdown(shutdown)
		_ = ds.Shutdown(shutdown)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failures:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return ErrUnavailable
		case <-ticker.C:
			pass, cancel := context.WithTimeout(ctx, 25*time.Second)
			_ = service.Reconcile(pass)
			cancel()
		}
	}
}
func readToken(p string) (string, error) {
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return "", ErrUnavailable
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", ErrUnavailable
	}
	v := strings.TrimSpace(string(b))
	if len(v) < 32 || strings.ContainsAny(v, "\r\n") {
		return "", ErrInvalid
	}
	return v, nil
}

type routeLease struct {
	handle  string
	expires time.Time
}
type siteRoutes struct {
	mu       sync.Mutex
	provider exposure.Provider
	cfg      *exposure.Config
	allowed  map[string]bool
	live     map[string]routeLease
}

func (r *siteRoutes) Ensure(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.allowed[name] {
		return ErrDenied
	}
	lease, exists := r.live[name]
	alive := true
	if p, ok := r.provider.(interface{ Alive(string) bool }); ok && exists {
		alive = p.Alive(lease.handle)
	}
	if exists && alive && time.Until(lease.expires) > time.Minute {
		return nil
	}
	if exists {
		if err := r.provider.Close(ctx, lease.handle); err != nil && !errors.Is(err, exposure.ErrNotFound) {
			return ErrUnavailable
		}
		delete(r.live, name)
	}
	lifetime := r.cfg.Lifetime()
	handle, err := r.provider.Open(ctx, exposure.Spec{Provider: "sish", Endpoint: name, Bind: name + "." + r.cfg.Zone, Target: "127.0.0.1:8181", PublicName: name + "." + r.cfg.Zone + "." + r.cfg.Relay.Domain, Idle: r.cfg.Idle(), Lifetime: lifetime})
	if err != nil {
		return ErrUnavailable
	}
	r.live[name] = routeLease{handle, time.Now().Add(lifetime)}
	return nil
}
func (r *siteRoutes) Remove(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lease, ok := r.live[name]
	if !ok {
		return nil
	}
	if err := r.provider.Close(ctx, lease.handle); err != nil && !errors.Is(err, exposure.ErrNotFound) {
		return ErrUnavailable
	}
	delete(r.live, name)
	return nil
}
func (r *siteRoutes) close() {
	for n := range r.allowed {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = r.Remove(ctx, n)
		cancel()
	}
}

// ReadControlToken reads a scoped local service credential without logging it.
func ReadControlToken(p string) (string, error) { return readToken(p) }

// InitVolume runs only in the socketless one-shot volume initializer.
func InitVolume(p string) error {
	if p != "/sites" {
		return ErrDenied
	}
	info, err := os.Lstat(p)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	if controllerInfo, err := os.Lstat("/controller"); err == nil {
		if !controllerInfo.IsDir() || controllerInfo.Mode()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		if os.Chown("/controller", 10001, 10001) != nil || os.Chmod("/controller", 0700) != nil {
			return ErrUnavailable
		}
	}
	if fleetInfo, err := os.Lstat("/fleet"); err == nil {
		if !fleetInfo.IsDir() || fleetInfo.Mode()&os.ModeSymlink != 0 || os.Chown("/fleet", 10001, 10001) != nil || os.Chmod("/fleet", 0700) != nil {
			return ErrUnavailable
		}
	}
	if zoneInfo, err := os.Lstat("/zone"); err == nil {
		if !zoneInfo.IsDir() || zoneInfo.Mode()&os.ModeSymlink != 0 || os.Chown("/zone", 10001, 10001) != nil || os.Chmod("/zone", 0700) != nil {
			return ErrUnavailable
		}
	}
	if os.Chown(p, 10001, 10001) != nil || os.Chmod(p, 0700) != nil {
		return ErrUnavailable
	}
	return nil
}

// The public hostname omits env, so only one env may own a user's hosting zone.
func acquireZoneLease(path string) (func() error, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalid
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, ErrDenied
	}
	lock := flock.New(path)
	ok, err := lock.TryLock()
	if err != nil {
		return nil, ErrUnavailable
	}
	if !ok {
		return nil, ErrConflict
	}
	return lock.Unlock, nil
}

func (cfg Deployment) brokerAuth() identity.Envelope {
	contextID := cfg.ContextID
	if contextID == "" {
		contextID = cfg.Owner
	}
	return identity.Envelope{Schema: identity.Schema, PrincipalID: cfg.Owner, ContextID: contextID, RuntimeID: cfg.Owner, ExternalIdentityID: cfg.Owner, Environment: cfg.Environment, PolicyVersion: cfg.PolicyVersion}
}
