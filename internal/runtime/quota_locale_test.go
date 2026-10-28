package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/locale"
)

// Historical assertions in this package pin Russian output.
func TestMain(m *testing.M) {
	_ = os.Setenv("HUB_DEFAULT_LANG", "ru")
	os.Exit(m.Run())
}

// /v1/quota renders subscription lines in the space's workspace language —
// the runtime resolves it from HUB_WORKSPACE preferences.
func TestQuotaLinesLocalized(t *testing.T) {
	ws := t.TempDir()
	if err := locale.StorePrefs(ws, locale.Prefs{Lang: locale.LangES, Source: locale.SourceChosen}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", ws)
	r := &relayRegistry{admin: "adm"}

	call := func() []string {
		req := httptest.NewRequest(http.MethodGet, "/v1/quota", nil)
		req.Header.Set("Authorization", "Bearer adm")
		w := httptest.NewRecorder()
		r.serveQuota(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("quota status %d", w.Code)
		}
		var payload struct {
			Lines []string `json:"lines"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Lines
	}

	// Unconfigured upstream.
	for _, key := range []string{"quota.unconfigured", "quota.unavailable", "quota.no-account", "quota.upstream", "quota.unparsed", "quota.title", "quota.reset"} {
		if _, ok := locale.Lookup(locale.LangES, key); !ok {
			t.Errorf("catalog missing es %q", key)
		}
	}
	if lines := call(); len(lines) != 1 || lines[0] != locale.T(locale.LangES, "quota.unconfigured") {
		t.Errorf("unconfigured=%v", lines)
	}

	// Management API fixture driving every outcome branch.
	var apiCall func() (int, string)
	var files string
	mgmt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(req.URL.Path, "/auth-files") {
			_, _ = w.Write([]byte(files))
			return
		}
		code, body := apiCall()
		data, _ := json.Marshal(map[string]any{"status_code": code, "body": body})
		_, _ = w.Write(data)
	}))
	defer mgmt.Close()
	t.Setenv("HUB_CLIPROXY_MGMT_URL", mgmt.URL)
	t.Setenv("HUB_CLIPROXY_MGMT_KEY", "k")

	files = `{"files":[]}`
	if lines := call(); len(lines) != 1 || !strings.Contains(lines[0], locale.T(locale.LangES, "quota.no-account")) {
		t.Errorf("no-account=%v", lines)
	}
	files = `{"files":[{"provider":"antigravity","auth_index":"a1","email":"u@x"}]}`

	apiCall = func() (int, string) { return http.StatusInternalServerError, "" }
	if lines := call(); len(lines) != 1 || !strings.Contains(lines[0], locale.T(locale.LangES, "quota.upstream", 500)) {
		t.Errorf("upstream=%v", lines)
	}
	apiCall = func() (int, string) { return http.StatusOK, "not-json" }
	if lines := call(); len(lines) != 1 || !strings.Contains(lines[0], locale.T(locale.LangES, "quota.unparsed")) {
		t.Errorf("unparsed=%v", lines)
	}
	apiCall = func() (int, string) {
		return http.StatusOK, `{"groups":[{"displayName":"Pro","buckets":[{"window":"5h","resetTime":"2030-01-01T00:00:00Z","remainingFraction":0.5}]}]}`
	}
	lines := call()
	if len(lines) != 2 || lines[0] != locale.T(locale.LangES, "quota.title", "u@x") || !strings.Contains(lines[1], locale.T(locale.LangES, "quota.reset", "01.01 00:00")) {
		t.Errorf("success=%v", lines)
	}
}
