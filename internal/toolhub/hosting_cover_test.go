package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/hosting"
)

func testHostingController(expires time.Time) (*genericController, hosting.Workload) {
	release := strings.Repeat("ab", 32)
	profile := HostingProfile{
		AdmissionDigest: "digest-1", Owner: "alice", Environment: "prod",
		Volume: "site-vol", Image: "registry.local/site@sha256:" + strings.Repeat("cd", 32), Runtime: "runsc",
	}
	c := &genericController{config: GenericControllerConfig{
		Hosting:             map[string]HostingProfile{"alice/prod": profile},
		HostingCellInit:     "/cell-init/hosting-cellinit",
		HostingHelperVolume: "cell-tools-vol",
		SeccompProfile:      "/seccomp/default.json",
	}}
	w := hosting.Workload{
		AdmissionDigest: "digest-1",
		Binding:         hosting.Binding{Owner: "alice", Environment: "prod"},
		Name:            "blog", Revision: 7, Release: release, ExpiresAt: expires,
	}
	return c, w
}

func TestHostingProfileAdmission(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("docker hosting admission paths are linux-only")
	}
	valid := time.Now().Add(time.Hour)
	c, w := testHostingController(valid)
	if _, err := c.hostingProfile(w); err != nil {
		t.Fatalf("valid profile: %v", err)
	}
	if _, err := c.hostingRemovalProfile(w); err != nil {
		t.Fatalf("removal profile: %v", err)
	}
	for _, mutate := range []func(*hosting.Workload){
		func(w *hosting.Workload) { w.Name = "Bad-Name" },
		func(w *hosting.Workload) { w.Name = "-lead" },
		func(w *hosting.Workload) { w.Name = "trail-" },
		func(w *hosting.Workload) { w.Name = "a_b" },
		func(w *hosting.Workload) { w.Revision = 0 },
		func(w *hosting.Workload) { w.Release = "short" },
		func(w *hosting.Workload) { w.Release = strings.Repeat("zz", 32) },
		func(w *hosting.Workload) { w.AdmissionDigest = "other" },
		func(w *hosting.Workload) { w.Binding.Owner = "mallory" },
		func(w *hosting.Workload) { w.ExpiresAt = time.Now().Add(-time.Hour) },
		func(w *hosting.Workload) { w.ExpiresAt = time.Now().Add(31 * 24 * time.Hour) },
	} {
		bad := w
		mutate(&bad)
		if _, err := c.hostingProfile(bad); !errors.Is(err, hosting.ErrDenied) {
			t.Fatalf("mutation admitted: %v", err)
		}
	}
	for _, mutate := range []func(*HostingProfile){
		func(p *HostingProfile) { p.Volume = "" },
		func(p *HostingProfile) { p.Volume = "a/b" },
		func(p *HostingProfile) { p.Image = "unpinned:latest" },
		func(p *HostingProfile) { p.Runtime = "runc" },
	} {
		c2, _ := testHostingController(valid)
		p := c2.config.Hosting["alice/prod"]
		mutate(&p)
		c2.config.Hosting["alice/prod"] = p
		if _, err := c2.hostingProfile(w); !errors.Is(err, hosting.ErrDenied) {
			t.Fatalf("profile mutation admitted: %v", err)
		}
	}
	badInit, _ := testHostingController(valid)
	badInit.config.HostingCellInit = "relative"
	if _, err := badInit.hostingProfile(w); !errors.Is(err, hosting.ErrDenied) {
		t.Fatal("relative cellinit admitted")
	}
	if got := siteObject(w); !strings.HasPrefix(got, "site-") {
		t.Fatalf("site object: %q", got)
	}
	if identitySiteName("") || identitySiteName(strings.Repeat("a", 41)) {
		t.Fatal("site name bounds broken")
	}
}

// siteInspectBody renders the single-container inspect document that
// satisfies every clause of the site profile contract.
func siteInspectBody(w hosting.Workload, p HostingProfile, helperVol, seccomp string, running bool) []byte {
	expires := strconv.FormatInt(w.ExpiresAt.Unix(), 10)
	doc := []map[string]any{{
		"Image": p.Image,
		"State": map[string]any{"Running": running, "ExitCode": 0},
		"Config": map[string]any{
			"Image":      p.Image,
			"User":       "10001:10001",
			"WorkingDir": "/work",
			"Entrypoint": []string{"/cell-tools/hosting-cellinit"},
			"Cmd":        []string{"site-go", expires},
			"Env":        []string{"PATH=/usr/bin", "LANG=C"},
			"Labels": map[string]string{
				"hermes-hub.owner":         w.Binding.Owner,
				"hermes-hub.site":          w.Name,
				"hermes-hub.site.env":      w.Binding.Environment,
				"hermes-hub.site.revision": strconv.FormatUint(w.Revision, 10),
				"hermes-hub.site.release":  w.Release,
			},
		},
		"HostConfig": map[string]any{
			"NanoCpus": 1e9, "Memory": 256 << 20, "MemorySwap": 256 << 20, "PidsLimit": 64,
			"Privileged": false, "ReadonlyRootfs": true, "NetworkMode": "none",
			"PidMode": "", "IpcMode": "", "Runtime": p.Runtime,
			"CapAdd": []string{}, "CapDrop": []string{"ALL"},
			"SecurityOpt":   []string{"no-new-privileges=true", "seccomp=" + seccomp},
			"LogConfig":     map[string]any{"Type": "none"},
			"RestartPolicy": map[string]any{"Name": "no"},
			"Devices":       []any{}, "DeviceRequests": []any{}, "VolumesFrom": []string{},
			"PortBindings": map[string]any{}, "PublishAllPorts": false,
			"Tmpfs": map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=32m"},
			"Mounts": []map[string]any{
				{"Type": "volume", "Source": p.Volume, "Target": "/work", "ReadOnly": true, "VolumeOptions": map[string]any{"Subpath": w.Release}},
				{"Type": "volume", "Source": helperVol, "Target": "/cell-tools", "ReadOnly": true},
			},
		},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"none": map[string]any{}}},
		"Mounts": []map[string]any{
			{"Destination": "/cell-tools", "RW": false, "Type": "volume", "Name": helperVol},
			{"Destination": "/work", "RW": false, "Type": "volume", "Name": p.Volume},
			{"Destination": "/tmp", "Type": "tmpfs"},
		},
	}}
	body, _ := json.Marshal(doc)
	return body
}

func TestSiteProfileContract(t *testing.T) {
	c, w := testHostingController(time.Now().Add(time.Hour))
	p := c.config.Hosting["alice/prod"]
	good := siteInspectBody(w, p, c.config.HostingHelperVolume, c.config.SeccompProfile, true)
	stub := func(body []byte, err error) func(context.Context, string, ...string) ([]byte, error) {
		return func(context.Context, string, ...string) ([]byte, error) { return body, err }
	}
	c.command = stub(good, nil)
	if err := c.siteProfile(context.Background(), w, p, true); err != nil {
		t.Fatalf("conforming profile: %v", err)
	}
	// Not running is denied for a running check.
	if err := c.siteProfile(context.Background(), w, p, false); !errors.Is(err, hosting.ErrDenied) {
		t.Fatalf("running-state mismatch: %v", err)
	}
	// Inspect failure, empty list and malformed JSON are unavailable.
	for _, bad := range []func(context.Context, string, ...string) ([]byte, error){
		stub(nil, errors.New("daemon down")),
		stub([]byte(`[]`), nil),
		stub([]byte(`not json`), nil),
	} {
		c.command = bad
		if err := c.siteProfile(context.Background(), w, p, true); !errors.Is(err, hosting.ErrUnavailable) {
			t.Fatalf("bad inspect admitted: %v", err)
		}
	}
	// Each contract clause fails closed — mutate one field per case.
	mutations := map[string]func(map[string]any){
		"privileged": func(m map[string]any) { m["HostConfig"].(map[string]any)["Privileged"] = true },
		"network":    func(m map[string]any) { m["HostConfig"].(map[string]any)["NetworkMode"] = "bridge" },
		"capdrop":    func(m map[string]any) { m["HostConfig"].(map[string]any)["CapDrop"] = []string{} },
		"seccomp": func(m map[string]any) {
			m["HostConfig"].(map[string]any)["SecurityOpt"] = []string{"no-new-privileges=true"}
		},
		"device": func(m map[string]any) { m["HostConfig"].(map[string]any)["Devices"] = []any{map[string]any{}} },
		"tmpfs-path": func(m map[string]any) {
			m["HostConfig"].(map[string]any)["Tmpfs"] = map[string]string{"/var": "size=32m"}
		},
		"mounts":    func(m map[string]any) { m["HostConfig"].(map[string]any)["Mounts"] = []map[string]any{} },
		"env-extra": func(m map[string]any) { m["Config"].(map[string]any)["Env"] = []string{"AWS_SECRET=x"} },
		"env-noeq":  func(m map[string]any) { m["Config"].(map[string]any)["Env"] = []string{"NOEQUALS"} },
		"label": func(m map[string]any) {
			m["Config"].(map[string]any)["Labels"].(map[string]any)["hermes-hub.site"] = "other"
		},
		"networks-2": func(m map[string]any) {
			m["NetworkSettings"] = map[string]any{"Networks": map[string]any{"none": map[string]any{}, "extra": map[string]any{}}}
		},
		"mount-rw": func(m map[string]any) {
			m["Mounts"].([]any)[0].(map[string]any)["RW"] = true
		},
	}
	for name, mutate := range mutations {
		var doc []map[string]any
		if err := json.Unmarshal(good, &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc[0])
		body, _ := json.Marshal(doc)
		c.command = stub(body, nil)
		if err := c.siteProfile(context.Background(), w, p, true); !errors.Is(err, hosting.ErrDenied) {
			t.Fatalf("%s mutation admitted", name)
		}
	}
	// A tmpfs entry without the hardening flags is denied.
	var doc []map[string]any
	if err := json.Unmarshal(good, &doc); err != nil {
		t.Fatal(err)
	}
	doc[0]["Mounts"] = []any{
		map[string]any{"Destination": "/cell-tools", "RW": false, "Type": "volume", "Name": c.config.HostingHelperVolume},
		map[string]any{"Destination": "/work", "RW": false, "Type": "volume", "Name": p.Volume},
	}
	doc[0]["HostConfig"].(map[string]any)["Tmpfs"] = map[string]string{"/tmp": "rw"}
	body, _ := json.Marshal(doc)
	c.command = stub(body, nil)
	if err := c.siteProfile(context.Background(), w, p, true); !errors.Is(err, hosting.ErrDenied) {
		t.Fatal("weak tmpfs admitted")
	}
	// Foreign mount destinations are denied.
	doc[0]["Mounts"] = append(doc[0]["Mounts"].([]any), map[string]any{"Destination": "/etc", "Type": "bind"})
	body, _ = json.Marshal(doc)
	c.command = stub(body, nil)
	if err := c.siteProfile(context.Background(), w, p, true); !errors.Is(err, hosting.ErrDenied) {
		t.Fatal("foreign mount admitted")
	}
}

func TestHostingHTTPGuardsAndDispatch(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("docker hosting admission paths are linux-only")
	}
	c, w := testHostingController(time.Now().Add(time.Hour))
	p := c.config.Hosting["alice/prod"]
	good := siteInspectBody(w, p, c.config.HostingHelperVolume, c.config.SeccompProfile, true)
	c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return good, nil
		}
		return nil, fmt.Errorf("unexpected %v", args)
	}
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/hosting", strings.NewReader(body))
		c.hostingHTTP(rec, req)
		return rec
	}
	if rec := post("not json"); rec.Code != http.StatusForbidden {
		t.Fatalf("bad json: %d", rec.Code)
	}
	if rec := post(`{"action":"check","workload":{}}`); rec.Code != http.StatusForbidden {
		t.Fatalf("empty workload: %d", rec.Code)
	}
	workload, _ := json.Marshal(w)
	if rec := post(fmt.Sprintf(`{"action":"check","workload":%s}`, workload)); rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body.String())
	}
	// Unknown action maps to the unavailable boundary.
	if rec := post(fmt.Sprintf(`{"action":"bogus","workload":%s}`, workload)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("bogus action: %d", rec.Code)
	}
	// Request without a live site is denied at the contract boundary.
	if rec := post(fmt.Sprintf(`{"action":"request","workload":%s,"request":{}}`, workload)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("request against live profile: %d", rec.Code)
	}
}

func TestHostingCapacity(t *testing.T) {
	c, _ := testHostingController(time.Now().Add(time.Hour))
	c.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("down") }
	if _, err := c.hostingCapacity(context.Background()); !errors.Is(err, hosting.ErrUnavailable) {
		t.Fatalf("daemon down: %v", err)
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte("1 1024"), nil }
	if _, err := c.hostingCapacity(context.Background()); !errors.Is(err, hosting.ErrUnavailable) {
		t.Fatalf("tiny host: %v", err)
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte("4 4294967296"), nil }
	n, err := c.hostingCapacity(context.Background())
	if err != nil || n != min(8, 3, (4096-1024)/256) {
		t.Fatalf("capacity: %d %v", n, err)
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte("64 68719476736"), nil }
	if n, _ = c.hostingCapacity(context.Background()); n != 8 {
		t.Fatalf("cap bound: %d", n)
	}
}
