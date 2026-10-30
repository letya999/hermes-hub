package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillRefLeaseReturnsOnlyRequestedAllowedNames(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "self-env.json"), []byte(`{"GITHUB_TOKEN":"owner-value","OTHER_TOKEN":"other-value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_SELF_ENV_KEYS", "GITHUB_TOKEN,OTHER_TOKEN")
	t.Setenv("HUB_PROTECTED_ENV_KEYS", "")
	t.Setenv("HUB_RUNTIME_AUTH", "test-auth")
	call := func(auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/skill-ref-lease", strings.NewReader(body))
		req.Header.Set("Authorization", auth)
		recorder := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(recorder, req)
		return recorder
	}
	if rec := call("", `{"refs":["GITHUB_TOKEN"]}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", rec.Code)
	}
	rec := call("Bearer test-auth", `{"refs":["GITHUB_TOKEN"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("lease status=%d", rec.Code)
	}
	var values map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values["GITHUB_TOKEN"] != "owner-value" {
		t.Fatal("lease was not limited to requested ref")
	}
	if rec := call("Bearer test-auth", `{"refs":["../self-env.json"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid ref status=%d", rec.Code)
	}
}
