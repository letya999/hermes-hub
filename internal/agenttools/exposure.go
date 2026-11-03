package agenttools

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/exposure"
)

// openExposure builds the opt-in exposure capability from the rendered
// environment. The feature is present only when the runtime carries
// HUB_EXPOSURE_CONFIG.
func (t *Tools) openExposure() {
	configPath := os.Getenv("HUB_EXPOSURE_CONFIG")
	if configPath == "" {
		return
	}
	cfg, err := exposure.Load(configPath)
	if err != nil {
		t.exposureErr = err
		return
	}
	if t.exposureProvider == nil {
		root, err := os.OpenRoot(filepath.Dir(configPath))
		if err != nil {
			t.exposureErr = err
			return
		}
		broker, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_RUNTIME_")
		if err != nil {
			t.exposureErr = err
			return
		}
		t.exposureProvider = exposure.NewSishProvider(cfg, exposure.ProviderOptions{
			Root:   root,
			Broker: broker,
			Auth:   runtimeEnvelope(),
			Public: os.Getenv("HUB_PUBLIC_TARGET"),
		})
	}
	ledger, err := audit.Open(filepath.Join(t.StateDir, "audit", "exposure.jsonl"))
	if err != nil {
		t.exposureErr = err
		return
	}
	if t.Exposure != nil {
		t.Exposure.Shutdown()
	}
	t.Exposure = exposure.NewManager(cfg, exposure.ManagerOptions{
		Provider: t.exposureProvider, Auth: runtimeEnvelope(), Ledger: ledger})
}

// setExposureProvider replaces the tunnel provider — tests inject fakes
// through it so the previous real provider's config root is released.
func (t *Tools) setExposureProvider(p exposure.Provider) {
	if c, ok := t.exposureProvider.(interface{ Shutdown() }); ok {
		c.Shutdown()
	}
	t.exposureProvider = p
}

// exposureService returns the live manager or the recorded init failure —
// the dispatch fail-closed path every call shares.
func (t *Tools) exposureService() (*exposure.Manager, error) {
	if t.Exposure != nil {
		return t.Exposure, nil
	}
	if t.exposureErr != nil {
		return nil, t.exposureErr
	}
	return nil, errors.New("exposure capability is not configured on this runtime")
}

func (t *Tools) exposureDispatch(ctx context.Context, name string, r Input) (any, error) {
	svc, err := t.exposureService()
	if err != nil {
		return nil, err
	}
	tunnelView := func(tn *exposure.Tunnel) map[string]any {
		return map[string]any{"endpoint": tn.Endpoint, "public_name": tn.PublicName,
			"target": tn.Target, "expires_at": tn.ExpiresAt, "uncertain": tn.Uncertain}
	}
	if name == "exposure_list" {
		live := svc.List()
		tunnels := make([]map[string]any, 0, len(live))
		for i := range live {
			tunnels = append(tunnels, tunnelView(&live[i]))
		}
		return map[string]any{"tunnels": tunnels}, nil
	}
	if name == "exposure_status" {
		tunnel, err := svc.Status(r.Name)
		if err != nil {
			return nil, err
		}
		return tunnelView(tunnel), nil
	}
	if name == "exposure_close" {
		if err := svc.Close(ctx, r.Name); err != nil {
			return nil, err
		}
		return map[string]any{"closed": r.Name}, nil
	}
	tunnel, err := svc.Open(ctx, r.Name)
	if err != nil {
		return nil, err
	}
	return tunnelView(tunnel), nil
}
