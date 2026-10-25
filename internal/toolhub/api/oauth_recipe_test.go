package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const fixtureOAuthManifest = `{"fields":[{"name":"CLIENT_JSON","type":"json","required":true,"secret":true,"delivery":"file","target":"/run/mcp-secrets/client.json"}],"oauth":{"authorize_url":"https://identity.example.com/authorize","token_url":"https://identity.example.com/token","scopes":["files.read"],"client_input":"CLIENT_JSON","client_sections":["application"],"state_target":"/home/mcp/session","token_file":"session.json","token_template":{"session":{"bearer":"$oauth.ACCESS_TOKEN","renew":"$oauth.REFRESH_TOKEN","expires":"$oauth.EXPIRES_AT_RFC3339"}},"probe_tool":"list-files"}}`

func TestOAuthManifestRejectsTrailingDocument(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/example/files", CommitSHA: strings.Repeat("a", 40)}
	_, _, _, err := ResolveLocalRecipe(map[string][]byte{"mcp-connection.json": []byte(fixtureOAuthManifest + `{}`)}, source)
	if err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestOAuthRecipeRejectsExpiredToken(t *testing.T) {
	var r ConnectionRecipe
	_ = json.Unmarshal([]byte(fixtureOAuthManifest), &r)
	_, err := r.OAuth.RenderToken(map[string]string{"EXPIRES_IN": "-1", "OAUTH_SCOPE": "files.read"}, "id", "secret", time.Now())
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("expired token accepted", err)
	}
	_, err = r.OAuth.RenderToken(map[string]string{"EXPIRES_IN": "9223372036854775807", "OAUTH_SCOPE": "files.read"}, "id", "secret", time.Now())
	if !errors.Is(err, ErrInvalid) {
		t.Error("overflowing expiry accepted", err)
	}
}

func TestPreparedOverlayPreservesOAuthContract(t *testing.T) {
	var r ConnectionRecipe
	_ = json.Unmarshal([]byte(fixtureOAuthManifest), &r)
	entry := PreparedEntry{OAuth: r.OAuth, Connection: r}
	if got := entry.Overlay(ConnectionRecipe{}); got.OAuth != entry.OAuth {
		t.Fatal("prepared OAuth missing from reviewed connection")
	}
	explicit := *r.OAuth
	explicit.Scopes = []string{"other.read"}
	if got := entry.Overlay(ConnectionRecipe{OAuth: &explicit}); got.OAuth != &explicit {
		t.Fatal("explicit OAuth overwritten")
	}
}

func TestOAuthRecipeBounds(t *testing.T) {
	var r ConnectionRecipe
	_ = json.Unmarshal([]byte(fixtureOAuthManifest), &r)
	r.OAuth.TokenFile = ".."
	if r.Validate() == nil {
		t.Error("parent directory accepted")
	}
	r.OAuth.TokenFile = "session.json"
	r.OAuth.TokenTemplate = map[string]any{"token": strings.Repeat("x", 65537)}
	if r.Validate() == nil {
		t.Error("oversized template accepted")
	}
	r = ConnectionRecipe{}
	_ = json.Unmarshal([]byte(fixtureOAuthManifest), &r)
	r.OAuth.TokenTemplate = map[string]any{"array": []any{"$oauth.UNKNOWN"}}
	if r.Validate() == nil {
		t.Error("unvalidated array template accepted")
	}
	r = ConnectionRecipe{}
	_ = json.Unmarshal([]byte(fixtureOAuthManifest), &r)
	r.OAuth.AccountInput = "ACCOUNT_EMAIL"
	if r.Validate() == nil {
		t.Error("undeclared account accepted")
	}
	r = ConnectionRecipe{}
	_ = json.Unmarshal([]byte(fixtureOAuthManifest), &r)
	_, err := r.OAuth.RenderToken(map[string]string{"EXPIRES_IN": "3600", "OAUTH_SCOPE": "files.read", "ACCESS_TOKEN": strings.Repeat("x", 65537)}, "id", "secret", time.Now())
	if err == nil {
		t.Error("oversized session accepted")
	}
}

func TestOAuthSourceSelectionPreservesSessionContract(t *testing.T) {
	var r ConnectionRecipe
	_ = json.Unmarshal([]byte(fixtureOAuthManifest), &r)
	r.Fields[0].Source = "mcp-connection.json"
	filtered, _ := FilterConnectionSource(r, "mcp-connection.json")
	if filtered.OAuth != r.OAuth {
		t.Error("OAuth lost selecting its client metadata")
	}
	filtered, _ = FilterConnectionSource(r, ".env.example")
	if filtered.OAuth != nil {
		t.Fatal("OAuth kept without client metadata")
	}
	source := ArtifactSource{Repository: "https://github.com/example/files", CommitSHA: strings.Repeat("a", 40)}
	_, _, _, err := ResolveLocalRecipe(map[string][]byte{"mcp-connection.json": []byte(strings.Replace(fixtureOAuthManifest, `"oauth":{`, `"oauth":{"typo":true,`, 1))}, source)
	if err == nil {
		t.Fatal("unknown OAuth metadata silently accepted")
	}
}

func TestOAuthRecipeFromPinnedSourceWithoutPreparedEntry(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/example/files-mcp", CommitSHA: strings.Repeat("a", 40)}
	files := map[string][]byte{"mcp-connection.json": []byte(fixtureOAuthManifest)}
	_, recipe, _, err := ResolveLocalRecipe(files, source)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(recipe)
	if err != nil || !strings.Contains(string(body), `"files.read"`) || !strings.Contains(string(body), `"$oauth.REFRESH_TOKEN"`) {
		t.Fatalf("OAuth recipe lost: %s, %v", body, err)
	}
	resolved, err := (RecipeResolver{}).Resolve(context.Background(), source, tarBytes(t, map[string]string{"mcp-connection.json": fixtureOAuthManifest}))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(resolved.Connection)
	if !strings.Contains(string(body), `"files.read"`) {
		t.Fatal("resolver discarded OAuth")
	}
}

func TestOAuthRecipeRequiresGrantedScopes(t *testing.T) {
	var r ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &r); err != nil {
		t.Fatal(err)
	}
	_, err := r.OAuth.RenderToken(map[string]string{"ACCESS_TOKEN": "fixture-access", "REFRESH_TOKEN": "fixture-refresh", "EXPIRES_IN": "3600", "OAUTH_SCOPE": "other.read"}, "fixture-id", "fixture-secret", time.Now())
	if !errors.Is(err, ErrReconsent) {
		t.Fatal("insufficient consent materialized", err)
	}
}

func TestDefinitionRejectsUnsafeOAuth(t *testing.T) {
	var r ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &r); err != nil {
		t.Fatal(err)
	}
	d := statefulContainerDefinition()
	d.OAuth = r.OAuth
	d.OAuth.TokenFile = "../session.json"
	if err := d.Validate(); err == nil {
		t.Fatal("definition accepted unsafe OAuth")
	}
}

func TestOAuthRecipeProviderSession(t *testing.T) {
	var recipe ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &recipe); err != nil {
		t.Fatal(err)
	}
	adapter, ok := any(recipe.OAuth).(interface {
		OAuthClient([]byte, string) (string, string, error)
		RenderToken(map[string]string, string, string, time.Time) ([]byte, error)
	})
	if !ok {
		t.Fatal("recipe cannot materialize a provider session")
	}
	id, secret, err := adapter.OAuthClient([]byte(`{"application":{"client_id":"fixture-id","client_secret":"fixture-secret"}}`), "http://127.0.0.1:8090/oauth/callback")
	if err != nil || id != "fixture-id" || secret != "fixture-secret" {
		t.Fatal("client extraction failed", err)
	}
	encoded, err := adapter.RenderToken(map[string]string{"ACCESS_TOKEN": "fixture-access", "REFRESH_TOKEN": "fixture-refresh", "EXPIRES_IN": "3600", "OAUTH_SCOPE": "files.read"}, id, secret, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var token struct {
		Session struct{ Bearer, Renew, Expires string }
	}
	if json.Unmarshal(encoded, &token) != nil || token.Session.Bearer != "fixture-access" || token.Session.Renew != "fixture-refresh" || token.Session.Expires != "2026-10-20T01:00:00Z" {
		t.Fatal("incorrect provider session")
	}
}

func TestOAuthRecipeRejectsUnsafeContracts(t *testing.T) {
	for name, change := range map[string][2]string{
		"http":            {"https://identity.example.com/token", "http://identity.example.com/token"},
		"token_backslash": {`"token_file":"session.json"`, `"token_file":"..\\session.json"`},
		"local_host":      {"https://identity.example.com/token", "https://metadata.internal/token"},
		"empty_host":      {"https://identity.example.com/token", "https://"},
		"private":         {"https://identity.example.com/token", "https://127.0.0.1/token"},
		"path":            {`"token_file":"session.json"`, `"token_file":"../session.json"`},
		"expression":      {"$oauth.REFRESH_TOKEN", "$exec.refresh"},
		"scope":           {`"scopes":["files.read"]`, `"scopes":[]`},
		"state":           {`"state_target":"/home/mcp/session"`, `"state_target":"../session"`},
		"userinfo":        {"https://identity.example.com/token", "https://user:password@identity.example.com/token"},
	} {
		t.Run(name, func(t *testing.T) {
			var recipe ConnectionRecipe
			if err := json.Unmarshal([]byte(strings.Replace(fixtureOAuthManifest, change[0], change[1], 1)), &recipe); err != nil {
				t.Fatal(err)
			}
			if err := recipe.Validate(); err == nil {
				t.Fatal("unsafe OAuth recipe accepted")
			}
		})
	}
}

func TestOAuthRecipeSurvivesDefinitionReview(t *testing.T) {
	var recipe ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &recipe); err != nil {
		t.Fatal(err)
	}
	var definition ToolDefinition
	MergeConnectionForDefinition(recipe, &definition)
	body, _ := json.Marshal(definition)
	if !strings.Contains(string(body), `"files.read"`) {
		t.Error("definition lost reviewed OAuth")
	}
	var config ArtifactImportConfig
	if err := json.Unmarshal([]byte(`{"OAuth":`+strings.SplitN(fixtureOAuthManifest, `"oauth":`, 2)[1][:len(strings.SplitN(fixtureOAuthManifest, `"oauth":`, 2)[1])-1]+`}`), &config); err != nil {
		t.Fatal(err)
	}
	first, err := ReviewDigest(ArtifactSource{}, ArtifactRecipe{}, StoredOCIArtifact{}, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReviewDigest(ArtifactSource{}, ArtifactRecipe{}, StoredOCIArtifact{}, ArtifactImportConfig{})
	if err != nil || first == second {
		t.Fatal("OAuth absent from immutable review")
	}
}

func TestOAuthRecipePublishedImportIsImmutable(t *testing.T) {
	source := ArtifactSource{Repository: "https://github.com/example/files-mcp", CommitSHA: strings.Repeat("a", 40)}
	var connection ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &connection); err != nil {
		t.Fatal(err)
	}
	r := RecipeResolution{Source: source, State: "ready", Connection: connection, Launch: LaunchRecipe{Transport: ContainerMCP, Artifact: "ghcr.io/example/files", Digest: "sha256:" + strings.Repeat("1", 64), Entrypoint: []string{"/app/server"}, Network: []string{"identity.example.com"}}, Evidence: []RecipeEvidence{{Source: "oci", Digest: "sha256:" + strings.Repeat("2", 64), Detail: "provenance"}, {Source: "oci", Digest: "sha256:" + strings.Repeat("3", 64), Detail: "sbom"}}}
	config := DefaultSelfInstallConfig(source)
	config.Workload.Stateful = true
	imported, err := ImportPublishedArtifact(source, config, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Definition.OAuth == nil {
		t.Fatal("published import lost OAuth")
	}
	imported.Artifact.ArchiveDigest = "sha256:" + strings.Repeat("4", 64)
	if err := ValidateImportedReview(imported); err != nil {
		t.Fatal(err)
	}
	imported.Definition.OAuth.TokenURL = "https://other.example.com/token"
	digest, err := ReviewDigestForImported(imported)
	if err != nil || digest == imported.Definition.Source.ReviewDigest {
		t.Fatal("changed issuer accepted with old review")
	}
}

func TestPreparedOAuthIsProviderNeutral(t *testing.T) {
	var entries []map[string]any
	if err := json.Unmarshal(PreparedCatalogJSON, &entries); err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &manifest); err != nil {
		t.Fatal(err)
	}
	a := manifest["oauth"].(map[string]any)
	a["client_input"] = "GOOGLE_OAUTH_CREDENTIALS"
	a["state_target"] = "/home/mcp/.config/google-calendar"
	a["token_file"] = "runtime_session.json"
	a["probe_tool"] = "list-calendars"
	entries[1]["oauth"] = a
	body, _ := json.Marshal(entries)
	parsed, err := ParsePreparedCatalog(body)
	if err != nil {
		t.Fatal("provider-neutral prepared recipe rejected", err)
	}
	config := parsed[1].Apply(DefaultSelfInstallConfig(parsed[1].Source))
	if config.OAuth == nil || config.OAuth.AuthorizeURL != "https://identity.example.com/authorize" {
		t.Fatal("prepared OAuth lost at import")
	}
}

func TestOAuthRecipeStandardTokenResponse(t *testing.T) {
	var recipe ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &recipe); err != nil {
		t.Fatal(err)
	}
	recipe.OAuth.TokenTemplate = map[string]any{"token_type": "$oauth.TOKEN_TYPE", "scope": "$oauth.OAUTH_SCOPE", "expiry_date": "$oauth.EXPIRES_AT_UNIX_MS"}
	if err := recipe.Validate(); err != nil {
		t.Fatal("standard token response rejected", err)
	}
	data, err := recipe.OAuth.RenderToken(map[string]string{"EXPIRES_IN": "3600", "TOKEN_TYPE": "Bearer", "OAUTH_SCOPE": "files.read"}, "", "", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if json.Unmarshal(data, &got) != nil || got["token_type"] != "Bearer" || got["scope"] != "files.read" || got["expiry_date"] != float64(3600000) {
		t.Fatal("standard response fields lost")
	}
}

func TestExplicitConnectionRecipeSurvivesResolution(t *testing.T) {
	var connection ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &connection); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), ConnectionRecipeContextKey{}, connection)
	source := ArtifactSource{Repository: "https://github.com/example/files-mcp", CommitSHA: strings.Repeat("a", 40)}
	resolution, err := (RecipeResolver{}).Resolve(ctx, source, tarBytes(t, map[string]string{"README.md": "Example MCP"}))
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Connection.OAuth == nil || resolution.Connection.OAuth.Scopes[0] != "files.read" {
		t.Fatal("explicit install recipe lost")
	}
}

func TestOAuthRecipeAuthorizedUserSession(t *testing.T) {
	var r ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &r); err != nil {
		t.Fatal(err)
	}
	r.OAuth.TokenTemplate = map[string]any{"token": "$oauth.ACCESS_TOKEN", "refresh_token": "$oauth.REFRESH_TOKEN", "token_uri": "$oauth.TOKEN_URL", "client_id": "$client.ID", "client_secret": "$client.SECRET", "scopes": "$oauth.SCOPES", "expiry": "$oauth.EXPIRES_AT_RFC3339"}
	if err := r.Validate(); err != nil {
		t.Error("authorized-user layout rejected", err)
	}
	data, err := r.OAuth.RenderToken(map[string]string{"ACCESS_TOKEN": "fixture-access", "REFRESH_TOKEN": "fixture-refresh", "EXPIRES_IN": "3600", "OAUTH_SCOPE": "files.read"}, "fixture-id", "fixture-secret", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Token, RefreshToken, TokenURI, ClientID, ClientSecret string
		Scopes                                                []string
	}
	var document map[string]any
	if json.Unmarshal(data, &document) != nil {
		t.Fatal("invalid session JSON")
	}
	if document["token"] != "fixture-access" || document["refresh_token"] != "fixture-refresh" || document["token_uri"] != "https://identity.example.com/token" || document["client_id"] != "fixture-id" || document["client_secret"] != "fixture-secret" {
		t.Error("authorized-user fields lost")
	}
	scopes, _ := document["scopes"].([]any)
	if len(scopes) != 1 || scopes[0] != "files.read" {
		t.Error("scope array lost")
	}
	_ = got
}

func TestOAuthRecipeRequiresClientFile(t *testing.T) {
	var r ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &r); err != nil {
		t.Fatal(err)
	}
	r.Fields = nil
	if err := r.Validate(); err == nil {
		t.Fatal("missing OAuth client input accepted")
	}
}

func TestOAuthDefinitionRequiresOwnerState(t *testing.T) {
	var r ConnectionRecipe
	if err := json.Unmarshal([]byte(fixtureOAuthManifest), &r); err != nil {
		t.Fatal(err)
	}
	d := statefulContainerDefinition()
	d.OAuth = r.OAuth
	d.Workload.Stateful = false
	if err := d.Validate(); err == nil {
		t.Fatal("OAuth admitted without durable owner state")
	}
}
