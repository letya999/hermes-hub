package toolhub

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/oauth"
)

// A failed prepared-OAuth callback must leave a diagnostic trace in the
// service log: today the handler flattens every completePreparedOAuth error
// into a bare 403 and the real cause is unrecoverable.
func TestOAuthCallbackFailureIsLogged(t *testing.T) {
	key, _ := credstore.GenerateKey()
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "s.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Control: &ControlPlane{OAuth: oauth.NewBroker(secrets, []string{"http://127.0.0.1/oauth/callback"}), Store: NewStore()}}
	var buf bytes.Buffer
	restore := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(restore)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8090/oauth/callback?state=st-dead&code=c-dead", nil)
	g.serveOAuthCallback(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("callback status=%d", rec.Code)
	}
	if !strings.Contains(buf.String(), "oauth callback") {
		t.Fatalf("callback failure left no diagnostic log: %q", buf.String())
	}
	if strings.Contains(buf.String(), "st-dead") || strings.Contains(buf.String(), "c-dead") {
		t.Fatalf("callback log leaked state/code material: %q", buf.String())
	}
}
