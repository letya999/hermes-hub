package toolhub

import (
	"context"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestInRuntimeControlCallbackDoesNotPinRegistry(t *testing.T) {
	for _, name := range []string{"goal_edit", "context_edit"} {
		t.Run(name, func(t *testing.T) {
			store := NewStore()
			definition := agentDefinition()
			family := map[string]string{"goal_edit": "goals", "context_edit": "context"}[name]
			definition.Tools = []api.ToolSpec{{Name: name, Effect: api.WriteEffect, CapabilityID: family + ".edit", Requires: []string{family + ".edit"}, Executor: "handler:" + family, Audience: api.AudienceModel, Description: "Control fixture", Uses: []api.CapabilityUse{{Action: "update", Resource: family}}}}
			if err := store.RegisterDefinition(definition); err != nil {
				t.Fatal(err)
			}
			auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
			if _, err := store.Enable(auth, definition.DefinitionID, definition.Version); err != nil {
				t.Fatal(err)
			}
			record := privateRunRecord(auth, "job-1", "run-1")
			record.Envelope.ConversationID = "telegram-7"
			gateway := &Gateway{Store: store, Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) { return record, nil }), Backend: api.AgentExecBackend{Exec: func(context.Context, api.EffectiveBinding, api.AgentExecRequest) (api.AgentExecResult, error) {
				updated := agentDefinition()
				updated.Version = "1.0.1"
				// A nested rights request can need this write lock after a store change.
				if err := store.RegisterDefinition(updated); err != nil {
					return api.AgentExecResult{}, err
				}
				return api.AgentExecResult{Result: map[string]any{"ok": true}}, nil
			}}}
			attachGatewayAudit(t, gateway)
			corr := api.HeaderCorrelation("job-1", "run-1")
			corr.ToolCallID = "control-call"
			ctx, cancel := context.WithTimeout(api.WithCallCorrelation(t.Context(), corr), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := gateway.call(ctx, auth, api.ProjectedToolName(definition.DefinitionID, definition.Version, name), map[string]any{})
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("control callback kept the registry admission lock")
			}
		})
	}
}
