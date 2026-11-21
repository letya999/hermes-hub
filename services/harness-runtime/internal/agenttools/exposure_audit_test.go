package agenttools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/exposure"
)

type auditFailExposureProvider struct {
	*fakeProvider
	path string
}

func (p *auditFailExposureProvider) Open(ctx context.Context, spec exposure.Spec) (string, error) {
	handle, err := p.fakeProvider.Open(ctx, spec)
	if err != nil {
		return handle, err
	}
	if err := os.Rename(p.path, p.path+".saved"); err != nil {
		return handle, err
	}
	if err := os.Mkdir(p.path, 0700); err != nil {
		return handle, err
	}
	return handle, nil
}

func TestExposureStatusProjectsUncertainAudit(t *testing.T) {
	v, p := exposureFixture(t)
	path := filepath.Join(v.StateDir, "audit", "exposure.jsonl")
	v.setExposureProvider(&auditFailExposureProvider{fakeProvider: p, path: path})
	v.openExposure()
	t.Cleanup(v.Exposure.Shutdown)
	if _, err := v.Call(context.Background(), "exposure_open", Input{Name: "demo"}); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("post effect=%v", err)
	}
	out, err := v.Call(context.Background(), "exposure_status", Input{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	status, ok := out.(map[string]any)
	if !ok || status["uncertain"] != true {
		t.Fatalf("uncertain status omitted: %+v", out)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
}
