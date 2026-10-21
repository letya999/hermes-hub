package toolhub

import (
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"testing"
)

func TestSynthesizeOAuthStateContract(t *testing.T) {
	d := statefulContainerDefinition()
	d.Credentials = []api.CredentialInput{{Name: "CLIENT_JSON", Required: true, Delivery: "file", Target: "/run/client.json"}}
	d.OAuth = &api.OAuthRecipe{ClientInput: "CLIENT_JSON", StateTarget: "/home/mcp/session", TokenFile: "session.json"}
	first, err := synthesizeContract(d)
	if err != nil {
		t.Fatal(err)
	}
	if first.State == nil || first.State.Target != d.OAuth.StateTarget || len(first.State.Files) != 1 || first.State.Files[0].Name != "session.json" || !first.State.Files[0].JSON {
		t.Error("dynamic contract lost OAuth state")
	}
	if len(first.Fields) != 1 || first.Fields[0].Kind != "json" || first.Fields[0].MaxBytes != 65536 {
		t.Error("OAuth client is not a bounded JSON file")
	}
	d.OAuth.TokenFile = "second.json"
	second, err := synthesizeContract(d)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Error("state contract absent from content identity")
	}
}
