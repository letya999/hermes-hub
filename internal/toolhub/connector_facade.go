package toolhub

import (
	"context"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// InvokeConnector runs one connector_* tool through the shared facade.
func (c *ControlPlane) InvokeConnector(ctx context.Context, auth identity.Envelope, tool string, args map[string]any) (api.OperationResult, error) {
	return api.OperationResult{State: api.OpStateFailed, Code: api.OpCodeNotPermitted, Summary: "not implemented"}, nil
}

func connectorToolContract(name string) (string, map[string]any) {
	return "connector tool " + name, map[string]any{"type": "object", "additionalProperties": true}
}
