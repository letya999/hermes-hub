package api

import (
	"testing"
	"time"
)

func TestConnectionRejectsUnrecognizedCredentialEvidence(t *testing.T) {
	connection := Connection{Schema: SchemaVersion, ConnectionID: "connection-1", DefinitionID: "google", Revision: 1, Status: ActiveStatus, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}}
	if err := connection.Validate(); err != nil {
		t.Fatal(err)
	}
	connection.CredentialVisibility = "free-form result from untrusted state"
	if err := connection.Validate(); err == nil {
		t.Fatal("unrecognized credential visibility accepted")
	}
}

func TestConnectionRejectsUnrecognizedHealthCause(t *testing.T) {
	connection := Connection{Schema: SchemaVersion, ConnectionID: "connection-1", DefinitionID: "google", Revision: 1, Status: ActiveStatus, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, CredentialVisibility: "unknown", CredentialCheckedAt: time.Now(), HealthCause: "unexpected operator instruction"}
	if err := connection.Validate(); err == nil {
		t.Fatal("unrecognized health cause accepted")
	}
}

func TestConnectionRequiresTimeForCredentialEvidence(t *testing.T) {
	connection := Connection{Schema: SchemaVersion, ConnectionID: "connection-1", DefinitionID: "google", Revision: 1, Status: ActiveStatus, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, CredentialVisibility: "unknown", HealthCause: "credential_visibility_unknown"}
	if err := connection.Validate(); err == nil {
		t.Fatal("credential check without time accepted")
	}
}

func TestConnectionRejectsCauseWithoutCredentialResult(t *testing.T) {
	connection := Connection{Schema: SchemaVersion, ConnectionID: "connection-1", DefinitionID: "google", Revision: 1, Status: ActiveStatus, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, HealthCause: "credential_check_unavailable"}
	if err := connection.Validate(); err == nil {
		t.Fatal("health cause without credential result accepted")
	}
}

func TestConnectionRejectsInvalidCredentialRetryState(t *testing.T) {
	connection := Connection{Schema: SchemaVersion, ConnectionID: "connection-1", DefinitionID: "google", Revision: 1, Status: ActiveStatus, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, CredentialRetryCount: 6}
	if err := connection.Validate(); err == nil {
		t.Fatal("retry count above bound accepted")
	}
	connection.CredentialRetryCount = 0
	connection.CredentialRetryAt = time.Now()
	if err := connection.Validate(); err == nil {
		t.Fatal("retry delay without attempts accepted")
	}
}

func TestConnectionRejectsInvalidRecipeRetryState(t *testing.T) {
	connection := Connection{Schema: SchemaVersion, ConnectionID: "connection-1", DefinitionID: "google", Revision: 1, Status: ActiveStatus, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, RecipeRetryCount: 6}
	if err := connection.Validate(); err == nil {
		t.Fatal("recipe retry count above bound accepted")
	}
	connection.RecipeRetryCount = 0
	connection.RecipeRetryAt = time.Now()
	if err := connection.Validate(); err == nil {
		t.Fatal("recipe retry delay without attempts accepted")
	}
}

func TestConnectionRejectsInvalidStageRetryState(t *testing.T) {
	for _, stage := range []struct {
		name  string
		set   func(*Connection, int, time.Time)
		reset func(*Connection)
	}{
		{"credential recipe", func(c *Connection, n int, at time.Time) { c.CredRecipeRetryCount, c.CredRecipeRetryAt = n, at }, func(c *Connection) { c.CredRecipeRetryCount, c.CredRecipeRetryAt = 0, time.Time{} }},
		{"surface", func(c *Connection, n int, at time.Time) { c.SurfaceRetryCount, c.SurfaceRetryAt = n, at }, func(c *Connection) { c.SurfaceRetryCount, c.SurfaceRetryAt = 0, time.Time{} }},
	} {
		t.Run(stage.name, func(t *testing.T) {
			connection := Connection{Schema: SchemaVersion, ConnectionID: "connection-1", DefinitionID: "google", Revision: 1, Status: ActiveStatus, Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}}
			stage.set(&connection, 6, time.Time{})
			if err := connection.Validate(); err == nil {
				t.Fatal("retry count above bound accepted")
			}
			stage.reset(&connection)
			stage.set(&connection, 0, time.Now())
			if err := connection.Validate(); err == nil {
				t.Fatal("retry delay without attempts accepted")
			}
		})
	}
}
