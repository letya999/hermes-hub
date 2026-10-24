package toolhub

import (
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestAuthFirstStatusSeparatesFormArtifactAndTools(t *testing.T) {
	c := &ControlPlane{Store: NewStore(), Now: time.Now}
	for _, tc := range []struct {
		name                string
		built, listed, form bool
	}{
		{"unbuilt-form", false, false, true}, {"built-form", true, false, true}, {"listed", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := statefulContainerDefinition()
			o := api.Onboarding{Definition: &d, AdmissionPending: !tc.listed, Phase: api.PhaseAwaitingCreds}
			if !tc.built {
				d.Source.Digest = ""
			}
			if !tc.listed {
				d.Tools = nil
			} else {
				o.Phase = api.PhaseAwaitingConfirm
			}
			if tc.form {
				o.BrokerAuthorizationURL = "http://127.0.0.1/connect/synthetic"
			}
			body := c.statusBody(o, false)
			for key, want := range map[string]bool{"form_ready": tc.form, "artifact_ready": tc.built, "tools_confirmed": tc.listed} {
				if got, ok := body[key].(bool); !ok || got != want {
					t.Errorf("%s=%v, want %v", key, body[key], want)
				}
			}
		})
	}
}
