package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestSpeakCallbackDoesNotPinRegistry(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	s, auth, policy, profile := managedStore(t)
	definition := r5ExecutorDefinition()
	definition.Tools = []api.ToolSpec{{Name: "media_speak", CapabilityID: "media.speak", Requires: []string{"channels.send_voice"},
		Effect: api.WriteEffect, Audience: api.AudienceModel, Executor: "runtime:channels", Description: "Queue speech", Arguments: []api.CLIArgument{{Name: "text", Type: "string", Required: true}}}}
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	digest := api.DefinitionDigest(definition)
	rule := api.CapabilityRule{CapabilityID: "media.speak", ImplementationDigest: digest, Action: "send", Resource: "channels", Limits: api.CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 5}}
	policy.Revision++
	policy.Ceiling = append(policy.Ceiling, rule)
	policy.Defaults = append(policy.Defaults, rule)
	policy.RightCeiling = append(policy.RightCeiling, api.RightRule{Capability: "channels", Operation: "send_voice"})
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.Rights = append(profile.Rights, api.RightRule{Capability: "channels", Operation: "send_voice"})
	profile.Selections = append(profile.Selections, api.CapabilitySelection{CapabilityID: "media.speak", DefinitionID: definition.DefinitionID,
		DefinitionVersion: definition.Version, ImplementationDigest: digest, ToolName: "media_speak", Name: "media_speak"})
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	grantDefinitionRights(t, s, auth, definition)
	if err := s.Save(filepath.Join(t.TempDir(), "store.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.CloseDB() })
	record := privateRunRecord(auth, "speech-job", "run-1")
	record.Envelope.ConversationID = "speech-conversation"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decision, err := s.Decide(auth, "channels", "send_voice", "", time.Now())
		if err != nil || !decision.Allow {
			t.Errorf("nested rights decision: allow=%t err=%v", decision.Allow, err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_id": "speech-delivery", "status": "queued"})
	}))
	defer server.Close()
	g := &Gateway{Store: s, CommunicationURL: server.URL, ControlToken: "service", Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) { return record, nil })}
	attachGatewayAudit(t, g)
	corr := api.HeaderCorrelation("speech-job", "run-1")
	corr.ToolCallID = "speech-call"
	ctx, cancel := context.WithTimeout(api.WithCallCorrelation(t.Context(), corr), 3*time.Second)
	defer cancel()
	result, err := g.call(ctx, auth, "media_speak", map[string]any{"text": "hello"})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("speech callback: result=%+v err=%v", result, err)
	}
}
