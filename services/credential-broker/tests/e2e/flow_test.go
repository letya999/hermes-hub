// Package e2e exercises the complete broker HTTP boundary with real disk state,
// separate signing identities and browser cookies. External SaaS/ToolHive are not
// claimed as tested: their integration belongs to the Hermes Hub deployment.
package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/app"
	"github.com/letya999/credential-broker/client"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
)

type installation struct {
	root   string
	cfg    app.Config
	url    string
	cancel context.CancelFunc
	done   chan error
	actor  identity.Actor
	t      *testing.T
}

func newInstallation(t *testing.T, c *contract.Contract) *installation {
	t.Helper()
	root := filepath.Join(t.TempDir(), "install")
	if e := app.Init(root); e != nil {
		t.Fatal(e)
	}
	cfg, e := app.ReadConfig(filepath.Join(root, "config.json"))
	if e != nil {
		t.Fatal(e)
	}
	if c != nil {
		if e = app.WriteJSON(filepath.Join(cfg.ContractsDir, c.ID+".json"), c); e != nil {
			t.Fatal(e)
		}
	}
	var a identity.Actor
	raw, e := os.ReadFile(filepath.Join(root, "actor.json"))
	if e != nil || json.Unmarshal(raw, &a) != nil {
		t.Fatal(e)
	}
	in := &installation{root: root, cfg: cfg, actor: a, t: t}
	in.start()
	t.Cleanup(in.stop)
	return in
}
func (i *installation) start() {
	i.t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		i.t.Fatal(e)
	}
	i.cfg.Listen = ln.Addr().String()
	i.cfg.PublicOrigin = "http://" + i.cfg.Listen
	i.url = i.cfg.PublicOrigin
	s, e := app.Build(i.cfg)
	if e != nil {
		i.t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	i.cancel = cancel
	i.done = make(chan error, 1)
	go func() { i.done <- s.Run(ctx, ln) }()
}
func (i *installation) stop() {
	i.t.Helper()
	if i.cancel == nil {
		return
	}
	i.cancel()
	select {
	case e := <-i.done:
		if e != nil {
			i.t.Error(e)
		}
	case <-time.After(3 * time.Second):
		i.t.Error("service failed graceful shutdown")
	}
	i.cancel = nil
}
func (i *installation) client(role string, a identity.Actor) *client.Client {
	i.t.Helper()
	priv, e := os.ReadFile(filepath.Join(i.root, "keys", role+".private"))
	if e != nil {
		i.t.Fatal(e)
	}
	aud := map[string]string{"toolhub": "broker:control", "communication": "broker:approve", "runtime": "broker:runtime"}[role]
	issuer := map[string]string{"toolhub": "hermes-toolhub", "communication": "hermes-communication", "runtime": "hermes-runtime-adapter"}[role]
	c, e := client.New(i.url, client.Signer{KeyID: role, Issuer: issuer, Audience: aud, PrivateKey: ed25519.PrivateKey(priv)}, a, nil)
	if e != nil {
		i.t.Fatal(e)
	}
	return c
}
func (i *installation) enroll(contractID string, files map[string][]byte) v1.Request {
	i.t.Helper()
	ctx := context.Background()
	ctl := i.client("toolhub", i.actor)
	r, e := ctl.CreateRequest(ctx, v1.CreateRequest{ContractID: contractID, ContractRevision: 1, ConnectionID: "account1", OnboardingID: "job1", IdempotencyKey: "end_to_end_1", OwnerKind: "user"})
	if e != nil {
		i.t.Fatal(e)
	}
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	page, e := browser.Get(r.AuthorizationURL)
	if e != nil {
		i.t.Fatal(e)
	}
	raw, e := io.ReadAll(page.Body)
	_ = page.Body.Close()
	if e != nil || page.StatusCode != 200 {
		i.t.Fatal(e, page.StatusCode)
	}
	found := regexp.MustCompile(`<p class="code">([A-Z2-7]{16})</p>`).FindSubmatch(raw)
	if len(found) != 2 {
		i.t.Fatal("no browser pairing code")
	}
	if e = i.client("communication", i.actor).Approve(ctx, r.ID, string(found[1])); e != nil {
		i.t.Fatal(e)
	}
	page, e = browser.Get(r.AuthorizationURL)
	if e != nil {
		i.t.Fatal(e)
	}
	raw, e = io.ReadAll(page.Body)
	_ = page.Body.Close()
	if e != nil {
		i.t.Fatal(e)
	}
	found = regexp.MustCompile(`name="csrf" value="([A-Z2-7]+)"`).FindSubmatch(raw)
	if len(found) != 2 {
		i.t.Fatal("no authorized form")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("csrf", string(found[1]))
	for field, value := range files {
		part, e := mw.CreateFormFile(field, "../../untrusted-name.json")
		if e != nil {
			i.t.Fatal(e)
		}
		if _, e = part.Write(value); e != nil {
			i.t.Fatal(e)
		}
	}
	_ = mw.Close()
	req, e := http.NewRequest("POST", r.AuthorizationURL+"/submit", &body)
	if e != nil {
		i.t.Fatal(e)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", i.url)
	res, e := browser.Do(req)
	if e != nil {
		i.t.Fatal(e)
	}
	raw, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		i.t.Fatal(res.StatusCode, string(raw))
	}
	if strings.Contains(string(raw), "SENTINEL_") {
		i.t.Fatal("secret reflected in browser")
	}
	r, e = ctl.Request(ctx, r.ID)
	if e != nil || r.Status != "ready" {
		i.t.Fatal(e, r)
	}
	return r
}
func (i *installation) grant(r v1.Request) (v1.Grant, identity.Actor) {
	i.t.Helper()
	in := v1.GrantRequest{ContractID: r.ContractID, ContractRevision: 1, PrincipalID: i.actor.PrincipalID, ContextID: i.actor.ContextID, RuntimeID: i.actor.RuntimeID, BindingID: "binding1", WorkloadID: "mcp1", Execution: "dedicated", IdempotencyKey: "e2e_grant"}
	g, e := i.client("toolhub", i.actor).Grant(context.Background(), r.CredentialID, in)
	if e != nil {
		i.t.Fatal(e)
	}
	a := i.actor
	a.BindingID = in.BindingID
	a.WorkloadID = in.WorkloadID
	return g, a
}

func TestE2EEnrollmentToRuntimeAndRevoke(t *testing.T) {
	i := newInstallation(t, nil)
	ctx := context.Background()
	r := i.enroll("github-pat", map[string][]byte{"token": []byte("SENTINEL_SYNTHETIC_PAT")})
	g, a := i.grant(r)
	ctl := i.client("toolhub", i.actor)
	rt := i.client("runtime", a)
	l, e := ctl.Acquire(ctx, v1.AcquireLease{GrantID: g.ID})
	if e != nil {
		t.Fatal(e)
	}
	m, e := rt.Materialize(ctx, l.ID)
	if e != nil || m.Env["GITHUB_PERSONAL_ACCESS_TOKEN"] != "SENTINEL_SYNTHETIC_PAT" {
		t.Fatal(e)
	}
	if _, e = ctl.Materialize(ctx, l.ID); e == nil {
		t.Fatal("ToolHub control client received secret")
	}
	wrong := i.actor
	wrong.PrincipalID = "bob"
	if _, e = i.client("toolhub", wrong).Acquire(ctx, v1.AcquireLease{GrantID: g.ID}); e == nil {
		t.Fatal("cross-user lease")
	}
	ev, e := ctl.Events(ctx, 0)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(ev)
	if bytes.Contains(raw, []byte("SENTINEL_SYNTHETIC_PAT")) {
		t.Fatal("ledger disclosure")
	}
	if _, e = rt.Renew(ctx, l.ID, 120); e != nil {
		t.Fatal(e)
	}
	if e = ctl.Revoke(ctx, r.CredentialID); e != nil {
		t.Fatal(e)
	}
	if _, e = rt.Materialize(ctx, l.ID); e == nil {
		t.Fatal("revoked lease remains usable")
	}
	// Inspect persisted bytes: files must contain ciphertext, not the canary value.
	if e = filepath.WalkDir(i.cfg.DataDir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if bytes.Contains(raw, []byte("SENTINEL_SYNTHETIC_PAT")) {
				t.Error("plaintext ledger")
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestE2EExactFileAndDurableRuntimeSession(t *testing.T) {
	c := contract.Contract{ID: "file-session", Revision: 1, Title: "Google-style file and auth state", Storage: "local", Fields: []contract.Field{{ID: "credentials", Kind: "json", Label: "Client JSON", Required: true, MaxBytes: 4096}}, Deliveries: []contract.Delivery{{Type: "file", Field: "credentials", Target: "/opt/mcp/config/GoogleServices.json", EnvName: "GOOGLE_OAUTH_CREDENTIALS"}}, State: &contract.State{Target: "/home/mcp/.config/calendar", Files: []contract.StateFile{{Name: "runtime_session.json", JSON: true, MaxBytes: 4096}}}}
	i := newInstallation(t, &c)
	ctx := context.Background()
	r := i.enroll(c.ID, map[string][]byte{"credentials": []byte(`{"installed":{"client_id":"SENTINEL_CLIENT"}}`)})
	g, a := i.grant(r)
	ctl := i.client("toolhub", i.actor)
	rt := i.client("runtime", a)
	l, e := ctl.Acquire(ctx, v1.AcquireLease{GrantID: g.ID})
	if e != nil {
		t.Fatal(e)
	}
	m, e := rt.Materialize(ctx, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	if m.Env["GOOGLE_OAUTH_CREDENTIALS"] != "/opt/mcp/config/GoogleServices.json" || m.Mounts[0].Target != "/opt/mcp/config/GoogleServices.json" || !m.Mounts[0].ReadOnly {
		t.Fatal(m)
	}
	raw, e := os.ReadFile(m.Mounts[0].Source)
	if e != nil || !bytes.Contains(raw, []byte("SENTINEL_CLIENT")) {
		t.Fatal(e)
	}
	stateDir := m.Mounts[1].Source
	if e = os.WriteFile(filepath.Join(stateDir, "runtime_session.json"), []byte(`{"refresh":"SENTINEL_SESSION"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = rt.Release(ctx, l.ID, v1.RuntimeRelease{Checkpoint: true, Quiesced: true}); e != nil {
		t.Fatal(e)
	}
	i.stop()
	i.start()
	ctl = i.client("toolhub", i.actor)
	rt = i.client("runtime", a)
	if _, e = rt.Materialize(ctx, l.ID); e == nil {
		t.Fatal("stale lease after restart")
	}
	l, e = ctl.Acquire(ctx, v1.AcquireLease{GrantID: g.ID})
	if e != nil {
		t.Fatal(e)
	}
	m, e = rt.Materialize(ctx, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile(filepath.Join(m.Mounts[1].Source, "runtime_session.json"))
	if e != nil || !bytes.Contains(raw, []byte("SENTINEL_SESSION")) {
		t.Fatal("session was not restored", e)
	}
	if e = rt.Release(ctx, l.ID, v1.RuntimeRelease{}); e != nil {
		t.Fatal(e)
	}
}
