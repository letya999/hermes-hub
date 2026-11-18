//go:build linux

package firecracker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/uno"
)

// bufConn reads bytes a bufio.Reader may have buffered ahead before falling
// through to the raw conn — the mux side must see frames the accept loop's
// reader already consumed.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// fakeGuest emulates the Firecracker VMM's host-side vsock UDS: it answers
// "CONNECT <port>\n" with "OK\n" and then speaks the guest agent protocol —
// exec on 5000, dial on 5001, mux frames on 5002. Real KVM is not required:
// the box only ever sees the unix socket contract.
type fakeGuest struct {
	ln      net.Listener
	exec    func(cmd string) (int, string, string)
	muxC    chan net.Conn
	dialRec chan string
}

func startFakeGuest(t *testing.T, uds string, execFn func(string) (int, string, string)) *fakeGuest {
	t.Helper()
	ln, err := net.Listen("unix", uds)
	if err != nil {
		t.Fatalf("fake guest listen %s: %v", uds, err)
	}
	g := &fakeGuest{
		ln:      ln,
		exec:    execFn,
		muxC:    make(chan net.Conn, 4),
		dialRec: make(chan string, 4),
	}
	go g.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return g
}

func (g *fakeGuest) serve() {
	for {
		c, err := g.ln.Accept()
		if err != nil {
			return
		}
		go g.handle(c)
	}
}

func (g *fakeGuest) handle(c net.Conn) {
	rd := bufio.NewReader(c)
	line, err := rd.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "CONNECT ") {
		_ = c.Close()
		return
	}
	port := strings.TrimSpace(strings.TrimPrefix(line, "CONNECT "))
	if _, err := fmt.Fprintf(c, "OK 0\n"); err != nil {
		_ = c.Close()
		return
	}
	switch port {
	case "5000":
		payload, err := rd.ReadString('\n')
		if err != nil {
			_ = c.Close()
			return
		}
		var req struct {
			Cmd string `json:"cmd"`
		}
		_ = json.Unmarshal([]byte(payload), &req)
		code, out, stderr := g.exec(req.Cmd)
		_ = json.NewEncoder(c).Encode(map[string]any{"exit_code": code, "stdout": out, "stderr": stderr})
		_ = c.Close()
	case "5001":
		target, err := rd.ReadString('\n')
		if err != nil {
			_ = c.Close()
			return
		}
		g.dialRec <- strings.TrimSpace(target)
		go func() {
			_, _ = io.Copy(c, rd)
			_ = c.Close()
		}()
	case "5002":
		g.muxC <- &bufConn{Conn: c, r: rd}
	default:
		_ = c.Close()
	}
}

// muxRecv reads one frame off the host-side mux conn.
func muxRecv(t *testing.T, c net.Conn) (byte, uint32, []byte) {
	t.Helper()
	hdr := make([]byte, 9)
	if _, err := io.ReadFull(c, hdr); err != nil {
		t.Fatalf("mux hdr: %v", err)
	}
	id, op, ln := binary.BigEndian.Uint32(hdr[:4]), hdr[4], binary.BigEndian.Uint32(hdr[5:9])
	payload := make([]byte, ln)
	if _, err := io.ReadFull(c, payload); err != nil {
		t.Fatalf("mux payload: %v", err)
	}
	return op, id, payload
}

// muxSend writes one frame from the "guest" into the box's muxReader.
func muxSend(t *testing.T, c net.Conn, op byte, id uint32, payload []byte) {
	t.Helper()
	hdr := make([]byte, 9)
	binary.BigEndian.PutUint32(hdr[:4], id)
	hdr[4] = op
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
	if _, err := c.Write(hdr); err != nil {
		t.Fatalf("mux send hdr: %v", err)
	}
	if len(payload) > 0 {
		if _, err := c.Write(payload); err != nil {
			t.Fatalf("mux send payload: %v", err)
		}
	}
}

// echoServer starts a TCP echo listener and returns its address.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func writeStubFCBin(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "fc-stub.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func testManager(t *testing.T) (*Manager, string) {
	t.Helper()
	work := t.TempDir()
	for _, name := range []string{"vmlinuz", "rootfs.img"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := NewManager(Config{
		Kernel:  filepath.Join(work, "vmlinuz"),
		RootFS:  filepath.Join(work, "rootfs.img"),
		FCBin:   writeStubFCBin(t, work),
		WorkDir: filepath.Join(work, "boxes"),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, work
}

// awaitBoxDir polls until the box working dir exists — the fake vsock UDS
// must appear at <dir>/v.sock before waitReady's first dial retry expires.
func awaitBoxDir(t *testing.T, work string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(filepath.Join(work, "boxes"))
		for _, e := range entries {
			if e.IsDir() {
				return filepath.Join(work, "boxes", e.Name())
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("box dir never appeared")
	return ""
}

func TestNewManagerValidation(t *testing.T) {
	work := t.TempDir()
	if _, err := NewManager(Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
	if _, err := NewManager(Config{Kernel: filepath.Join(work, "missing"), RootFS: "x", FCBin: "x"}); err == nil {
		t.Fatal("missing kernel accepted")
	}
	kernel := filepath.Join(work, "vmlinuz")
	if err := os.WriteFile(kernel, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(Config{Kernel: kernel, RootFS: "x", FCBin: "x"}); err == nil {
		t.Fatal("missing rootfs accepted")
	}
	rootfs := filepath.Join(work, "rootfs.img")
	if err := os.WriteFile(rootfs, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fcbin := writeStubFCBin(t, work)
	if _, err := NewManager(Config{Kernel: kernel, RootFS: rootfs, FCBin: fcbin, WorkDir: filepath.Join(work, "x", "y")}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestShareSizeMB(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if mb := shareSizeMB(dir); mb <= 64 {
		t.Fatalf("shareSizeMB too small: %d", mb)
	}
	// Unreadable path still yields the 128 MB floor via du failure.
	if mb := shareSizeMB(filepath.Join(dir, "nope")); mb < 128+42+64 {
		t.Fatalf("fallback size wrong: %d", mb)
	}
}

func TestLaunchRejectsBadRequest(t *testing.T) {
	m, _ := testManager(t)
	for _, req := range []uno.LaunchRequest{
		{Name: "", VCPU: 2, RAMMB: 512},
		{Name: "b", VCPU: 0, RAMMB: 512},
		{Name: "b", VCPU: 2, RAMMB: 64},
	} {
		if _, err := m.Launch(context.Background(), req); err == nil {
			t.Fatalf("invalid launch accepted: %+v", req)
		}
	}
}

// TestLaunchRunDeleteFlow drives the full box lifecycle against the fake
// guest: Launch (waitReady→exec handshake, openMux, forwards), SubmitRun,
// mux proxy frames, and Delete.
func TestLaunchRunDeleteFlow(t *testing.T) {
	m, work := testManager(t)
	echo := echoServer(t)

	var guest *fakeGuest
	launchDone := make(chan uno.Box, 1)
	launchErr := make(chan error, 1)
	go func() {
		box, err := m.Launch(context.Background(), uno.LaunchRequest{
			Name: "box-a", VCPU: 1, RAMMB: 256,
			Proxies:  []uno.Proxy{{GuestPort: 8080, Target: echo}},
			Forwards: []uno.Forward{{GuestPort: 9000}},
			Env:      map[string]string{"HUB_FC_PROXIES": "8080", "HUB_FC_INGRESS": "9000"},
			Labels:   map[string]string{"k": "v"},
		})
		if err != nil {
			launchErr <- err
			return
		}
		launchDone <- box
	}()

	// The VMM-side UDS appears shortly after the box dir; install the fake
	// guest before waitReady's 15s vsock dial window closes.
	boxDir := awaitBoxDir(t, work)
	guest = startFakeGuest(t, filepath.Join(boxDir, "v.sock"), func(cmd string) (int, string, string) {
		if cmd == "fail" {
			return 7, "", "boom"
		}
		return 0, "ok:" + cmd, ""
	})

	var launched uno.Box
	select {
	case launched = <-launchDone:
	case err := <-launchErr:
		t.Fatalf("launch: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("launch timed out")
	}
	if launched.Status != "running" {
		t.Fatalf("box status = %q err=%q", launched.Status, launched.Error)
	}
	if launched.Address == "" || launched.VsockUDS == "" || launched.Labels["k"] != "v" {
		t.Fatalf("launch result incomplete: %+v", launched)
	}

	// Get/List see the running box.
	if got, ok := m.Get(launched.ID); !ok || got.Status != "running" {
		t.Fatalf("Get: %+v ok=%v", got, ok)
	}
	if _, ok := m.Get("nope"); ok {
		t.Fatal("Get returned unknown box")
	}
	if list := m.List(); len(list) != 1 || list[0].ID != launched.ID {
		t.Fatalf("List: %+v", list)
	}

	// SubmitRun: exec round-trip through the fake guest agent.
	run, err := m.SubmitRun(context.Background(), launched.ID, uno.RunRequest{Cmd: "true", IdempotencyKey: "k1", WaitSeconds: 3})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}
	if run.Status != "succeeded" || run.ExitCode != 0 || run.Stdout != "ok:true" {
		t.Fatalf("run result: %+v", run)
	}
	// Idempotent replay returns the recorded run, not a new one.
	replay, err := m.SubmitRun(context.Background(), launched.ID, uno.RunRequest{Cmd: "true", IdempotencyKey: "k1"})
	if err != nil || replay.ID != run.ID {
		t.Fatalf("idempotent replay: %+v err=%v", replay, err)
	}
	// A failing exec lands as a failed run; missing box is an error.
	run2, err := m.SubmitRun(context.Background(), launched.ID, uno.RunRequest{Cmd: "fail", WaitSeconds: 3})
	if err != nil || run2.Status != "failed" || run2.ExitCode != 7 || run2.Stderr != "boom" {
		t.Fatalf("failed run: %+v err=%v", run2, err)
	}
	if _, err := m.SubmitRun(context.Background(), "ghost", uno.RunRequest{Cmd: "x"}); err != errBoxGone {
		t.Fatalf("SubmitRun on missing box: %v", err)
	}
	// GetRun: found, long-poll, and misses.
	got, ok := m.GetRun(launched.ID, run.ID, 0)
	if !ok || got.ID != run.ID {
		t.Fatalf("GetRun: %+v ok=%v", got, ok)
	}
	if _, ok := m.GetRun(launched.ID, "nope", 0); ok {
		t.Fatal("GetRun returned unknown run")
	}
	if _, ok := m.GetRun("ghost", run.ID, 1); ok {
		t.Fatal("GetRun on missing box")
	}

	// Mux: the guest opens a proxied conn, data flows both ways through the
	// box to the allowlisted host echo target, then op=3 drops it.
	var mux net.Conn
	select {
	case mux = <-guest.muxC:
	case <-time.After(10 * time.Second):
		t.Fatal("mux conn never arrived")
	}
	muxSend(t, mux, 1, 42, []byte("8080"))
	muxSend(t, mux, 2, 42, []byte("ping"))
	op, id, payload := muxRecv(t, mux)
	if op != 2 || id != 42 || string(payload) != "ping" {
		t.Fatalf("mux echo frame: op=%d id=%d payload=%q", op, id, payload)
	}
	muxSend(t, mux, 3, 42, nil)
	// Open on an unregistered guest port is refused with an immediate close.
	// The dropped echo conn's pump also emits a close frame for id 42, so
	// accept either order by waiting for the id=43 close specifically.
	muxSend(t, mux, 1, 43, []byte("9999"))
	_ = mux.SetReadDeadline(time.Now().Add(10 * time.Second))
	sawClose43 := false
	for !sawClose43 {
		op, id, _ = muxRecv(t, mux)
		if op == 3 && id == 43 {
			sawClose43 = true
		}
	}
	_ = mux.SetReadDeadline(time.Time{})
	// An oversized frame header kills the mux reader (defensive bound).
	muxSend(t, mux, 2, 1, nil) // stray data on unknown conn: tolerated
	hdr := make([]byte, 9)
	binary.BigEndian.PutUint32(hdr[5:9], 1<<21)
	if _, err := mux.Write(hdr); err != nil {
		t.Fatalf("oversized hdr write: %v", err)
	}

	// Forward: host TCP forward accepts, dials the guest 5001 service which
	// records the "host:port" target line, then echoes bytes end to end.
	fc, err := net.DialTimeout("tcp", launched.Address, 5*time.Second)
	if err != nil {
		t.Fatalf("forward dial: %v", err)
	}
	if _, err := fc.Write([]byte("hello\n")); err != nil {
		t.Fatalf("forward write: %v", err)
	}
	buf := make([]byte, 6)
	if _, err := io.ReadFull(fc, buf); err != nil || string(buf) != "hello\n" {
		t.Fatalf("forward echo: %v buf=%q", err, buf)
	}
	_ = fc.Close()
	select {
	case target := <-guest.dialRec:
		if target != "127.0.0.1:9000" {
			t.Fatalf("guest dial target = %q", target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guest never saw forward target line")
	}

	// Delete kills the VMM stub, closes forwards/mux, removes state.
	if !m.Delete(launched.ID) {
		t.Fatal("Delete returned false")
	}
	if m.Delete(launched.ID) {
		t.Fatal("second Delete returned true")
	}
	if _, err := os.Stat(boxDir); !os.IsNotExist(err) {
		t.Fatalf("box dir survived delete: %v", err)
	}
	if _, ok := m.Get(launched.ID); ok {
		t.Fatal("box still listed after delete")
	}
}

// TestLaunchFailureModes covers fail() transitions: guest agent never ready,
// and a share image that cannot be built.
func TestLaunchFailureModes(t *testing.T) {
	m, _ := testManager(t)
	// No fake guest: waitReady burns through the exec retry loop until the
	// box's proc-exit watcher fires — the stub exits in 300s, far too long,
	// so bound the failure with a cancelled context.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	box, err := m.Launch(ctx, uno.LaunchRequest{Name: "no-guest", VCPU: 1, RAMMB: 128})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if box.Status != "failed" || box.Error == "" {
		t.Fatalf("expected failed box: %+v", box)
	}
	m.Delete(box.ID)
}

// TestExecAndMuxWithoutConn covers the direct error paths.
func TestExecAndMuxWithoutConn(t *testing.T) {
	b := &box{mux: nil, conns: map[uint32]net.Conn{}}
	if err := b.sendFrame(2, 1, []byte("x")); err == nil {
		t.Fatal("sendFrame without mux succeeded")
	}
	b.dropConn(9) // no-op on missing conn
}

// TestHTTPAPI exercises the Uno-compatible REST surface end to end.
func TestHTTPAPI(t *testing.T) {
	work := t.TempDir()
	for _, name := range []string{"vmlinuz", "rootfs.img"} {
		if err := os.WriteFile(filepath.Join(work, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := NewManager(Config{
		Kernel:  filepath.Join(work, "vmlinuz"),
		RootFS:  filepath.Join(work, "rootfs.img"),
		FCBin:   writeStubFCBin(t, work),
		WorkDir: filepath.Join(work, "boxes"),
		Token:   "tok",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m)
	defer srv.Close()

	do := func(method, path, body string, auth bool) (*http.Response, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, srv.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		if auth {
			req.Header.Set("Authorization", "Bearer tok")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return resp, v
	}

	// Auth gate.
	if resp, _ := do("GET", "/v1/boxes", "", false); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: %d", resp.StatusCode)
	}
	// Empty list, then bad and good creates.
	if resp, v := do("GET", "/v1/boxes", "", true); resp.StatusCode != 200 || len(v["boxes"].([]any)) != 0 {
		t.Fatalf("GET /boxes: %d %v", resp.StatusCode, v)
	}
	if resp, _ := do("POST", "/v1/boxes", "{bad json", true); resp.StatusCode != 400 {
		t.Fatalf("bad create body: %d", resp.StatusCode)
	}
	if resp, _ := do("POST", "/v1/boxes", `{"name":"","vcpu":0,"ram_mb":0}`, true); resp.StatusCode != 500 {
		t.Fatalf("invalid create: %d", resp.StatusCode)
	}
	// Unknown/missing resources.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/boxes/nope"},
		{"DELETE", "/v1/boxes/nope"},
		{"GET", "/v1/boxes/nope/runs"},
		{"GET", "/v1/boxes/nope/runs/r1"},
		{"GET", "/v1/mystery"},
	} {
		if resp, _ := do(tc.method, tc.path, "", true); resp.StatusCode != 404 {
			t.Fatalf("%s %s: %d", tc.method, tc.path, resp.StatusCode)
		}
	}
	if resp, _ := do("POST", "/v1/boxes/nope/runs", `{"cmd":"x"}`, true); resp.StatusCode != 404 {
		t.Fatalf("run on missing box: %d", resp.StatusCode)
	}
	if resp, _ := do("GET", "/v1/boxes/nope/runs/r1?wait_s=1", "", true); resp.StatusCode != 404 {
		t.Fatalf("waited run on missing box: %d", resp.StatusCode)
	}

	// Inject a live box with a recorded run, then hit the per-box routes.
	b := &box{
		box:    uno.Box{ID: "box-9", Status: "running"},
		dir:    filepath.Join(work, "boxes", "box-9"),
		exited: make(chan struct{}),
		runs:   map[string]*uno.Run{"r1": {ID: "r1", Status: "succeeded"}},
		conns:  map[uint32]net.Conn{},
	}
	m.mu.Lock()
	m.boxes["box-9"] = b
	m.mu.Unlock()

	if resp, v := do("GET", "/v1/boxes/box-9", "", true); resp.StatusCode != 200 {
		t.Fatalf("GET box: %d %v", resp.StatusCode, v)
	}
	if resp, v := do("GET", "/v1/boxes/box-9/runs", "", true); resp.StatusCode != 200 || len(v["runs"].([]any)) != 1 {
		t.Fatalf("GET runs: %d %v", resp.StatusCode, v)
	}
	if resp, v := do("GET", "/v1/boxes/box-9/runs/r1", "", true); resp.StatusCode != 200 || v["run"] == nil {
		t.Fatalf("GET run: %d %v", resp.StatusCode, v)
	}
	if resp, v := do("GET", "/v1/boxes/box-9/runs/r1?wait_s=1", "", true); resp.StatusCode != 200 || v["run"] == nil {
		t.Fatalf("GET run wait: %d %v", resp.StatusCode, v)
	}
	// Submit a real run against a box whose guest never answers — the run
	// records as failed once exec's vsock dial gives up.
	if resp, v := do("POST", "/v1/boxes/box-9/runs", `{"cmd":"x","wait_s":1}`, true); resp.StatusCode != 200 || v["run"] == nil {
		t.Fatalf("POST run: %d %v", resp.StatusCode, v)
	}
	close(b.exited)
	if resp, _ := do("DELETE", "/v1/boxes/box-9", "", true); resp.StatusCode != 200 {
		t.Fatalf("DELETE box: %d", resp.StatusCode)
	}
}

// TestVsockDialRefused covers the retry-then-deadline path: the UDS exists
// but always rejects CONNECT.
func TestVsockDialRefused(t *testing.T) {
	work := t.TempDir()
	uds := filepath.Join(work, "v.sock")
	ln, err := net.Listen("unix", uds)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				rd := bufio.NewReader(c)
				_, _ = rd.ReadString('\n')
				_, _ = fmt.Fprintf(c, "ERR nope\n")
				_ = c.Close()
			}()
		}
	}()
	b := &box{dir: work}
	start := time.Now()
	if _, err := b.vsockDial(5000); err == nil {
		t.Fatal("vsockDial succeeded against refusal")
	}
	if elapsed := time.Since(start); elapsed < 14*time.Second || elapsed > 20*time.Second {
		t.Fatalf("vsockDial deadline off: %v", elapsed)
	}
}

// TestWaitReadyExited covers the "vm exited during provisioning" branch.
func TestWaitReadyExited(t *testing.T) {
	b := &box{dir: t.TempDir(), exited: make(chan struct{})}
	close(b.exited)
	if err := b.waitReady(context.Background()); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("waitReady on exited box: %v", err)
	}
	// Context cancellation surfaces too — exited stays open but the dial
	// keeps failing until ctx expires.
	b2 := &box{dir: t.TempDir(), exited: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b2.waitReady(ctx); err == nil {
		t.Fatal("waitReady without guest succeeded")
	}
}

// TestSyncShareBack covers the readonly early-return and the mount-failure
// path (loop mounting a non-ext4 file fails, which is fine — it's logged).
func TestSyncShareBack(t *testing.T) {
	syncShareBack(shareDisk{ro: true, img: "x", src: "y"})
	work := t.TempDir()
	img := filepath.Join(work, "not-ext4.img")
	if err := os.WriteFile(img, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	syncShareBack(shareDisk{img: img, src: filepath.Join(work, "src")})
}

// TestShareImageBuild exercises the mkfs path inside launch when available.
func TestShareImageBuild(t *testing.T) {
	if _, err := os.Stat("/sbin/mkfs.ext4"); err != nil {
		if _, err2 := os.Stat("/usr/sbin/mkfs.ext4"); err2 != nil {
			t.Skip("mkfs.ext4 unavailable")
		}
	}
	m, _ := testManager(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "seed.txt"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	// launch builds the share image before spawning the VMM — but the VMM
	// stub needs no fake guest until waitReady, which we skip by calling
	// m.launch directly.
	lb, err := m.launch(context.Background(), uno.LaunchRequest{
		Name: "shared", VCPU: 1, RAMMB: 128,
		Shares: []uno.Share{{HostPath: src}},
	})
	if err != nil {
		t.Fatalf("launch with share: %v", err)
	}
	defer func() {
		if lb.proc != nil && lb.proc.Process != nil {
			_ = lb.proc.Process.Kill()
		}
	}()
	if len(lb.shareDisks) != 1 || lb.shareDisks[0].dev != "/dev/vdb" {
		t.Fatalf("share disks: %+v", lb.shareDisks)
	}
	if _, err := os.Stat(lb.shareDisks[0].img); err != nil {
		t.Fatalf("share image missing: %v", err)
	}
}
