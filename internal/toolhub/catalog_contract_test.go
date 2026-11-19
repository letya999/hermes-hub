package toolhub

import (
	"net/http"
	"testing"

	toolsurface "github.com/letya999/hermes-hub/contracts/toolsurface/v1"
	"github.com/letya999/hermes-hub/contracts/toolsurface/v1/conformance"
)

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	store, auth, binding := seededStore(t)
	server := CatalogServer{Store: store}
	known := []toolsurface.CatalogEntry{{Name: "google-work", Version: "1.0.0", Transport: "remote-mcp", WorkloadClass: "per-user", Tools: []string{"search", "send"}, BindingID: binding.ToolBindingID, Status: "active", Enabled: true}}
	conformance.Both(t, server, func(token string) http.Handler { return toolsurface.Handler(server, token) }, auth, known)
}
