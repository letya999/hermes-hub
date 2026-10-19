package toolhub

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Credential form helpers render the reviewed hint set: input types, the
// Google OAuth dual-file/manual shape, alternative groups collapsed behind
// <details>, and loopback-only OAuth callbacks.
func TestCredentialFormHelpers(t *testing.T) {
	hint := api.CredentialHint{Name: "API_TOKEN", Type: "string", Secret: true, Hint: "token from the console"}
	if got := credentialFieldMarkup(hint); !strings.Contains(got, `type="password"`) || !strings.Contains(got, "token from the console") {
		t.Fatalf("secret string markup: %s", got)
	}
	if got := credentialFieldMarkup(api.CredentialHint{Name: "EMAIL", Type: "email"}); !strings.Contains(got, `type="email"`) || !strings.Contains(got, `autocomplete="email"`) {
		t.Fatalf("email markup: %s", got)
	}
	if got := credentialFieldMarkup(api.CredentialHint{Name: "CALLBACK", Type: "url"}); !strings.Contains(got, `type="url"`) {
		t.Fatalf("url markup: %s", got)
	}
	if got := credentialFieldMarkup(api.CredentialHint{Name: "COUNT", Type: "integer"}); !strings.Contains(got, `type="number"`) {
		t.Fatalf("number markup: %s", got)
	}
	if got := credentialFieldMarkup(api.CredentialHint{Name: "PLAIN", Type: "string"}); !strings.Contains(got, `type="text"`) {
		t.Fatalf("plain markup: %s", got)
	}
	if got := credentialFieldMarkup(api.CredentialHint{Name: "PAYLOAD", Type: "json", Delivery: "json"}); !strings.Contains(got, "<textarea") {
		t.Fatalf("json markup: %s", got)
	}
	oauth := api.CredentialHint{Name: "GOOGLE_OAUTH_CREDENTIALS"}
	if got := credentialFieldMarkup(oauth); !strings.Contains(got, "google_oauth_file") || !strings.Contains(got, "google_oauth_client_id") {
		t.Fatalf("oauth markup: %s", got)
	}
	if got := credentialFieldLabel(oauth); !strings.Contains(got, "OAuth") {
		t.Fatalf("oauth label: %s", got)
	}
	if got := credentialFieldLabel(api.CredentialHint{Name: "API_KEY"}); got != "API KEY" {
		t.Fatalf("label: %q", got)
	}
	if got := credentialFieldHint(oauth); !strings.Contains(got, "gcp-oauth") {
		t.Fatalf("oauth hint: %s", got)
	}
	if got := credentialFieldHint(api.CredentialHint{Name: "X", Hint: "protected loopback form"}); got != "" {
		t.Fatalf("internal hint leaked: %q", got)
	}
	if got := credentialFieldHint(api.CredentialHint{Name: "X", Alternative: "use OAuth instead"}); !strings.Contains(got, "use OAuth instead") {
		t.Fatalf("alternative hint: %s", got)
	}

	// Alternative groups render one open form plus collapsed others; plain
	// fields ride inside every group form.
	var b strings.Builder
	hints := []api.CredentialHint{
		{Name: "ACCOUNT_ID", Type: "string"},
		{Name: "API_TOKEN", Type: "string", Secret: true, AlternativeGroup: 1, Alternative: "token auth"},
		{Name: "EMAIL", Type: "email", AlternativeGroup: 2},
		{Name: "PASSWORD", Type: "string", Secret: true, AlternativeGroup: 2},
	}
	writeCredentialFields(&b, hints, nil, "nonce-1")
	page := b.String()
	if !strings.Contains(page, `value="nonce-1"`) || strings.Count(page, "<form") != 2 || !strings.Contains(page, "<details>") {
		t.Fatalf("grouped form: %s", page)
	}
	b.Reset()
	writeCredentialFields(&b, []api.CredentialHint{hint}, nil, "n")
	if strings.Contains(b.String(), "<details>") || !strings.Contains(b.String(), "API_TOKEN") {
		t.Fatalf("plain form: %s", b.String())
	}

	values := map[string]string{}
	applyCredentialDefaults([]api.CredentialHint{{Name: "HOST"}, {Name: "PORT"}, {Name: "TOKEN"}}, values)
	if values["HOST"] != "127.0.0.1" || values["PORT"] != "3000" || values["TOKEN"] != "" {
		t.Fatalf("defaults: %v", values)
	}

	code, title, _ := credentialSubmitPage(fmt.Errorf("%w: %w", errCredentialSaved, errors.New("upstream timeout")))
	if code != http.StatusServiceUnavailable || title != "Подключение не завершено" {
		t.Fatalf("saved-but-unfinished page: %d %q", code, title)
	}
	code, title, body := credentialSubmitPage(fmt.Errorf("%w: form nonce mismatch", api.ErrUnauthorized))
	if code != http.StatusUnauthorized || title != "Ссылка устарела" || !strings.Contains(body, "не сохранены") {
		t.Fatalf("expired link page: %d %q %q", code, title, body)
	}
	code, _, body = credentialSubmitPage(fmt.Errorf("%w: missing TOKEN", api.ErrInvalid))
	if code != http.StatusBadRequest || !strings.Contains(body, "обязательное поле") {
		t.Fatalf("invalid page: %d %q", code, body)
	}
	code, _, body = credentialSubmitPage(errors.New("probe exploded"))
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "Токен не сохранён") {
		t.Fatalf("probe failure page: %d %q", code, body)
	}

	if got := credentialFormRejection(errors.New("incomplete credential alternative: EMAIL,PASSWORD")); !strings.Contains(got, "EMAIL") || !strings.Contains(got, "PASSWORD") {
		t.Fatalf("incomplete alternative text: %q", got)
	}
	if got := credentialFormRejection(errors.New("choose one credential alternative")); !strings.Contains(got, "один полный набор") {
		t.Fatalf("choose one text: %q", got)
	}
	if got := credentialFormRejection(errors.New("unrecognized credential alternative")); !strings.Contains(got, "рецепт") {
		t.Fatalf("unrecognized text: %q", got)
	}
	if got := credentialProbeFailure(errors.New(`rpc: {"message":"invalid client credentials"}`)); got != "invalid client credentials" {
		t.Fatalf("probe failure text: %q", got)
	}
	if got := credentialProbeFailure(errors.New("tools/list empty")); got != "Сервер не ответил списком инструментов." {
		t.Fatalf("empty list text: %q", got)
	}

	loop := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/x", nil)
	if !loopbackHTTP(loop) {
		t.Fatal("loopback request not recognized")
	}
	remote := httptest.NewRequest(http.MethodGet, "http://example.com/x", nil)
	remote.RemoteAddr = "203.0.113.10:5050"
	if loopbackHTTP(remote) {
		t.Fatal("public address treated as loopback")
	}
}
