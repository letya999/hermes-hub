package communication

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSessionControlReportsNextTurnBoundary(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	created := destinationRequest(g, http.MethodPost, "/v1/sessions", `{"action":"create","title":"next-turn"}`)
	var response map[string]any
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &response) != nil || response["effective_from"] != "next_turn" {
		t.Fatalf("create boundary: %d %s", created.Code, created.Body.String())
	}
	session := response["session"].(map[string]any)
	id := session["id"].(string)
	switched := destinationRequest(g, http.MethodPost, "/v1/sessions", `{"action":"switch","id":"`+id+`"}`)
	response = nil
	if switched.Code != http.StatusOK || json.Unmarshal(switched.Body.Bytes(), &response) != nil || response["effective_from"] != "next_turn" || response["switched_to"] != id {
		t.Fatalf("switch boundary: %d %s", switched.Code, switched.Body.String())
	}
}
