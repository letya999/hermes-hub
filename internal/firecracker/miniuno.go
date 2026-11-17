//go:build linux

// Package firecracker implements mini-Uno: a small Uno-compatible control
// plane that manages real Firecracker microVMs on a KVM host. It exists so
// the firecracker compute backend can be qualified locally (WSL2) with the
// same API contract a production Uno deployment exposes.
package firecracker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/uno"
)

// Config configures the daemon.
type Config struct {
	Listen      string // e.g. 127.0.0.1:8320
	Token       string
	Kernel      string // path to guest kernel (vmlinuz)
	RootFS      string // path to rootfs image (ext4)
	WorkDir     string // per-box sockets/logs
	ForwardBase string // host listen address for forward ports
	FCBin       string // firecracker binary
	VirtioFSD   string // virtiofsd binary
}

// box is one managed VM.
type box struct {
	mu         sync.Mutex
	box        uno.Box
	proc       *exec.Cmd
	dir        string
	cid        uint32
	uds        string
	exited     chan struct{}
	errMsg     string
	fwds       []net.Listener
	mux        net.Conn
	muxMu      sync.Mutex
	runs       map[string]*uno.Run
	conns      map[uint32]net.Conn
	proxies    map[int]string
	shareDisks []shareDisk
}

// shareDisk is one per-box ext4 image attached as a virtio-blk drive.
type shareDisk struct {
	img string
	dev string
	src string
	ro  bool
}

// shareSizeMB sizes the seed image: directory contents + 30% headroom.
func shareSizeMB(dir string) int {
	out, err := exec.Command("du", "-sm", dir).Output()
	mb := 128
	if err == nil {
		var du int
		if _, err := fmt.Sscanf(string(out), "%d", &du); err == nil && du > 0 {
			mb = du
		}
	}
	return mb + mb/3 + 64
}

// Manager owns all boxes on this host.
type Manager struct {
	cfg    Config
	mu     sync.Mutex
	boxes  map[string]*box
	cidSeq atomic.Uint32
	runSeq atomic.Uint64
	boxSeq atomic.Uint64
}

// NewManager validates config and returns a manager.
func NewManager(cfg Config) (*Manager, error) {
	for _, p := range []string{cfg.Kernel, cfg.RootFS, cfg.FCBin} {
		if p == "" {
			return nil, errors.New("firecracker: kernel, rootfs, fc binary and virtiofsd paths are required")
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("firecracker: %s: %w", p, err)
		}
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, boxes: map[string]*box{}}
	m.cidSeq.Store(3)
	return m, nil
}

func (m *Manager) newBox(req uno.LaunchRequest) (*box, error) {
	id := fmt.Sprintf("box-%d", m.boxSeq.Add(1))
	dir := filepath.Join(m.cfg.WorkDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b := &box{
		box: uno.Box{ID: id, Name: req.Name, Image: req.Image, Status: "provisioning", Labels: req.Labels, CreatedAt: time.Now().UTC()},
		dir: dir, cid: m.cidSeq.Add(1), exited: make(chan struct{}),
		runs: map[string]*uno.Run{}, conns: map[uint32]net.Conn{}, proxies: map[int]string{},
	}
	b.box.VsockUDS = b.udsPath()
	for _, p := range req.Proxies {
		b.proxies[p.GuestPort] = p.Target
	}
	return b, nil
}

type fsDev struct {
	sock string
	tag  string
}

func (m *Manager) launch(ctx context.Context, req uno.LaunchRequest) (*box, error) {
	b, err := m.newBox(req)
	if err != nil {
		return nil, err
	}
	// Shares are per-box ext4 disk images seeded from the host directory —
	// a real virtio-blk device (PVC-equivalent). On delete, rw shares are
	// synced back to their source directory.
	var shareDisks []shareDisk
	os.Remove(b.udsPath())
	for i, sh := range req.Shares {
		img := filepath.Join(b.dir, fmt.Sprintf("share-%d.img", i))
		sizeMB := shareSizeMB(sh.HostPath)
		out, err := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-d", sh.HostPath, "-b", "4096", img, fmt.Sprintf("%dM", sizeMB)).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("share image %s: %w: %s", sh.HostPath, err, strings.TrimSpace(string(out)))
		}
		shareDisks = append(shareDisks, shareDisk{img: img, dev: fmt.Sprintf("/dev/vd%c", 'b'+i), src: sh.HostPath, ro: sh.ReadOnly})
	}
	cleanup := func() {
		os.RemoveAll(b.dir)
	}
	bootArgs := "console=ttyS0 reboot=k panic=1 pci=off init=/fc-init"
	if proxies := req.Env["HUB_FC_PROXIES"]; proxies != "" {
		bootArgs += " fc.proxies=" + proxies
	}
	if ingress := req.Env["HUB_FC_INGRESS"]; ingress != "" {
		bootArgs += " fc.ingress=" + ingress
	}
	var drives []any
	drives = append(drives, map[string]any{"drive_id": "rootfs", "path_on_host": m.cfg.RootFS, "is_root_device": true, "is_read_only": false})
	for i, sd := range shareDisks {
		drives = append(drives, map[string]any{"drive_id": fmt.Sprintf("share%d", i), "path_on_host": sd.img, "is_root_device": false, "is_read_only": sd.ro})
	}
	cfg := map[string]any{
		"boot-source":    map[string]any{"kernel_image_path": m.cfg.Kernel, "boot_args": bootArgs},
		"drives":         drives,
		"machine-config": map[string]any{"vcpu_count": req.VCPU, "mem_size_mib": req.RAMMB},
		"vsock":          map[string]any{"guest_cid": b.cid, "uds_path": b.udsPath()},
	}
	raw, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(b.dir, "fc.json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		cleanup()
		return nil, err
	}
	logFile, _ := os.Create(filepath.Join(b.dir, "console.log"))
	b.proc = exec.CommandContext(context.Background(), m.cfg.FCBin, "--no-api", "--config-file", cfgPath)
	b.proc.Stdout, b.proc.Stderr = logFile, logFile
	b.proc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := b.proc.Start(); err != nil {
		logFile.Close()
		cleanup()
		return nil, fmt.Errorf("firecracker start: %w", err)
	}
	b.shareDisks = shareDisks
	// The firecracker vsock UDS is root-owned; the supervisor's control relay
	// sidecar (uid 10001) mounts this dir and dials it, so open it up.
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(b.udsPath()); err == nil {
				_ = os.Chmod(b.dir, 0o755)
				_ = os.Chmod(b.udsPath(), 0o666)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	go func() {
		b.proc.Wait()
		b.mu.Lock()
		if b.status() == "running" || b.status() == "provisioning" {
			b.setStatus("failed", "vm exited")
		}
		b.mu.Unlock()
		close(b.exited)
	}()
	return b, nil
}

func (b *box) udsPath() string { return filepath.Join(b.dir, "v.sock") }
func (b *box) status() string  { return b.box.Status }
func (b *box) setStatus(s, e string) {
	b.box.Status, b.box.Error = s, e
}

// vsockDial opens a host->guest vsock stream via the firecracker uds.
func (b *box) vsockDial(port uint32) (net.Conn, error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, err := net.DialTimeout("unix", b.udsPath(), 3*time.Second)
		if err == nil {
			c.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err == nil {
				rd := bufio.NewReader(c)
				line, err := rd.ReadString('\n')
				if err == nil && strings.HasPrefix(line, "OK") {
					c.SetDeadline(time.Time{})
					return &prefixConn{Conn: c, r: rd}, nil
				}
			}
			c.Close()
			err = fmt.Errorf("vsock connect %d refused", port)
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(300 * time.Millisecond)
	}
}

type prefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *prefixConn) Read(b []byte) (int, error) { return p.r.Read(b) }

// exec runs a command inside the guest via the agent exec service (:5000).
func (b *box) exec(ctx context.Context, req uno.RunRequest) (*uno.Run, error) {
	c, err := b.vsockDial(5000)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	payload, _ := json.Marshal(map[string]any{"cmd": req.Cmd, "env": req.Env, "timeout_s": req.TimeoutSeconds})
	if _, err := c.Write(append(payload, '\n')); err != nil {
		return nil, err
	}
	var resp struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := json.NewDecoder(io.LimitReader(c, 8<<20)).Decode(&resp); err != nil {
		return nil, err
	}
	r := &uno.Run{ExitCode: resp.ExitCode, Stdout: resp.Stdout, Stderr: resp.Stderr}
	if resp.ExitCode == 0 {
		r.Status = "succeeded"
	} else {
		r.Status = "failed"
	}
	return r, nil
}

// waitReady blocks until the guest agent answers exec.
func (b *box) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := b.exec(ctx, uno.RunRequest{Cmd: "true", TimeoutSeconds: 5}); err == nil {
			return nil
		}
		select {
		case <-b.exited:
			return errors.New("vm exited during provisioning")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if time.Now().After(deadline) {
			return errors.New("guest agent did not become ready")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// openMux establishes the persistent mux conn to agent :5002 and pumps frames.
func (b *box) openMux() error {
	c, err := b.vsockDial(5002)
	if err != nil {
		return err
	}
	b.mux = c
	go b.muxReader(c)
	return nil
}

// mux frame: [4B connID][1B op][4B len][payload]
// ops: 1=open(payload "port"), 2=data, 3=close
func (b *box) muxReader(c net.Conn) {
	hdr := make([]byte, 9)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		id, op, ln := binary.BigEndian.Uint32(hdr[:4]), hdr[4], binary.BigEndian.Uint32(hdr[5:9])
		if ln > 1<<20 {
			return
		}
		payload := make([]byte, ln)
		if _, err := io.ReadFull(c, payload); err != nil {
			return
		}
		switch op {
		case 1:
			var gp int
			fmt.Sscanf(string(payload), "%d", &gp)
			target, ok := b.proxies[gp]
			if !ok {
				b.sendFrame(3, id, nil)
				continue
			}
			tc, err := net.DialTimeout("tcp", target, 5*time.Second)
			if err != nil {
				b.sendFrame(3, id, nil)
				continue
			}
			b.mu.Lock()
			b.conns[id] = tc
			b.mu.Unlock()
			go func(id uint32, tc net.Conn) {
				buf := make([]byte, 32*1024)
				for {
					n, err := tc.Read(buf)
					if n > 0 {
						if err := b.sendFrame(2, id, buf[:n]); err != nil {
							break
						}
					}
					if err != nil {
						break
					}
				}
				b.sendFrame(3, id, nil)
				b.dropConn(id)
			}(id, tc)
		case 2:
			b.mu.Lock()
			tc := b.conns[id]
			b.mu.Unlock()
			if tc != nil {
				if _, err := tc.Write(payload); err != nil {
					b.dropConn(id)
				}
			}
		case 3:
			b.dropConn(id)
		}
	}
}

func (b *box) sendFrame(op byte, id uint32, payload []byte) error {
	b.muxMu.Lock()
	defer b.muxMu.Unlock()
	if b.mux == nil {
		return errors.New("no mux")
	}
	hdr := make([]byte, 9)
	binary.BigEndian.PutUint32(hdr[:4], id)
	hdr[4] = op
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
	if _, err := b.mux.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := b.mux.Write(payload)
		return err
	}
	return nil
}

func (b *box) dropConn(id uint32) {
	b.mu.Lock()
	tc := b.conns[id]
	delete(b.conns, id)
	b.mu.Unlock()
	if tc != nil {
		tc.Close()
	}
}

// startForwards opens a host TCP listener per forward rule -> guest :5001 dial.
func (b *box) startForwards(req uno.LaunchRequest) error {
	for _, f := range req.Forwards {
		bind := f.Bind
		if bind == "" {
			bind = "127.0.0.1"
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(f.HostPort)))
		if err != nil {
			return err
		}
		b.fwds = append(b.fwds, ln)
		addr := ln.Addr().String()
		go func(ln net.Listener, gp int) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					up, err := b.vsockDial(5001)
					if err != nil {
						c.Close()
						return
					}
					fmt.Fprintf(up, "127.0.0.1:%d\n", gp)
					bridge(c, up)
				}()
			}
		}(ln, f.GuestPort)
		b.box.Address = addr
	}
	return nil
}

func bridge(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
}

// Launch creates, boots, and readies a box synchronously.
func (m *Manager) Launch(ctx context.Context, req uno.LaunchRequest) (uno.Box, error) {
	if req.Name == "" || req.VCPU < 1 || req.RAMMB < 128 {
		return uno.Box{}, errors.New("name, vcpu>=1 and ram_mb>=128 are required")
	}
	b, err := m.launch(ctx, req)
	if err != nil {
		return uno.Box{}, err
	}
	m.mu.Lock()
	m.boxes[b.box.ID] = b
	m.mu.Unlock()
	fail := func(e error) (uno.Box, error) {
		b.setStatus("failed", e.Error())
		return b.box, nil
	}
	if err := b.waitReady(ctx); err != nil {
		return fail(err)
	}
	if err := b.openMux(); err != nil {
		return fail(err)
	}
	if err := b.startForwards(req); err != nil {
		return fail(err)
	}
	b.mu.Lock()
	b.setStatus("running", "")
	b.mu.Unlock()
	return b.box, nil
}

// Get returns a box by id.
func (m *Manager) Get(id string) (uno.Box, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.boxes[id]
	if !ok {
		return uno.Box{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.box, true
}

// List returns all boxes.
func (m *Manager) List() []uno.Box {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]uno.Box, 0, len(m.boxes))
	for _, b := range m.boxes {
		b.mu.Lock()
		out = append(out, b.box)
		b.mu.Unlock()
	}
	return out
}

// Delete kills and removes a box.
func (m *Manager) Delete(id string) bool {
	m.mu.Lock()
	b, ok := m.boxes[id]
	delete(m.boxes, id)
	m.mu.Unlock()
	if !ok {
		return false
	}
	// Best-effort flush: guest page cache must reach the share image before
	// the VMM is killed, or recent writes are lost.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	b.exec(ctx, uno.RunRequest{Cmd: "sync", TimeoutSeconds: 8})
	cancel()
	if b.proc != nil && b.proc.Process != nil {
		b.proc.Process.Kill()
	}
	for _, ln := range b.fwds {
		ln.Close()
	}
	if b.mux != nil {
		b.mux.Close()
	}
	<-b.exited
	for _, sd := range b.shareDisks {
		syncShareBack(sd)
	}
	os.RemoveAll(b.dir)
	return true
}

// syncShareBack loop-mounts the share image and copies contents back to
// the source directory so guest writes reach the host space dir.
func syncShareBack(sd shareDisk) {
	if sd.ro {
		return
	}
	mnt, err := os.MkdirTemp("", "fc-share-")
	if err != nil {
		return
	}
	defer os.Remove(mnt)
	if out, err := exec.Command("mount", "-o", "loop", sd.img, mnt).CombinedOutput(); err != nil {
		log.Printf("mini-uno: share mount %s: %v: %s", sd.img, err, strings.TrimSpace(string(out)))
		return
	}
	defer exec.Command("umount", mnt).Run()
	if out, err := exec.Command("cp", "-a", mnt+"/.", sd.src+"/").CombinedOutput(); err != nil {
		log.Printf("mini-uno: share sync back %s: %v: %s", sd.img, err, strings.TrimSpace(string(out)))
	}
}

// SubmitRun records and executes a run; idempotent by key.
func (m *Manager) SubmitRun(ctx context.Context, boxID string, req uno.RunRequest) (*uno.Run, error) {
	m.mu.Lock()
	b, ok := m.boxes[boxID]
	m.mu.Unlock()
	if !ok {
		return nil, errBoxGone
	}
	key := req.IdempotencyKey
	b.mu.Lock()
	for _, r := range b.runs {
		if key != "" && r.IdempotencyKey == key {
			cur := *r
			b.mu.Unlock()
			return &cur, nil
		}
	}
	run := &uno.Run{ID: fmt.Sprintf("run-%d", m.runSeq.Add(1)), Status: "running", IdempotencyKey: key}
	b.runs[run.ID] = run
	b.mu.Unlock()
	go func() {
		res, err := b.exec(ctx, req)
		b.mu.Lock()
		defer b.mu.Unlock()
		if err != nil {
			run.Status, run.Stderr = "failed", err.Error()
			run.ExitCode = -1
			return
		}
		run.Status, run.ExitCode, run.Stdout, run.Stderr = res.Status, res.ExitCode, res.Stdout, res.Stderr
	}()
	if req.WaitSeconds > 0 {
		m.waitRun(b, run, time.Duration(req.WaitSeconds)*time.Second)
	}
	return run, nil
}

// GetRun returns a run, optionally long-polling for a terminal state.
func (m *Manager) GetRun(boxID, runID string, waitS int) (*uno.Run, bool) {
	m.mu.Lock()
	b, ok := m.boxes[boxID]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	if waitS > 0 {
		m.waitRun(b, nil, time.Duration(waitS)*time.Second)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.runs[runID]
	if !ok {
		return nil, false
	}
	cur := *r
	return &cur, true
}

func (m *Manager) waitRun(b *box, _ *uno.Run, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		done := true
		for _, r := range b.runs {
			if r.Status == "running" {
				done = false
			}
		}
		b.mu.Unlock()
		if done {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var errBoxGone = errors.New("box not found")

// ServeHTTP exposes the Uno-compatible REST API.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.cfg.Token != "" && r.Header.Get("Authorization") != "Bearer "+m.cfg.Token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	write := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == http.MethodGet && path == "/boxes":
		write(200, map[string]any{"boxes": m.List()})
	case r.Method == http.MethodPost && path == "/boxes":
		var req uno.LaunchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			write(400, map[string]string{"error": err.Error()})
			return
		}
		box, err := m.Launch(r.Context(), req)
		if err != nil {
			write(500, map[string]string{"error": err.Error()})
			return
		}
		write(200, map[string]any{"box": box})
	case len(parts) == 2 && parts[0] == "boxes" && r.Method == http.MethodGet:
		if box, ok := m.Get(parts[1]); ok {
			write(200, map[string]any{"box": box})
		} else {
			write(404, map[string]string{"error": "not found"})
		}
	case len(parts) == 2 && parts[0] == "boxes" && r.Method == http.MethodDelete:
		if m.Delete(parts[1]) {
			write(200, map[string]string{"status": "deleted"})
		} else {
			write(404, map[string]string{"error": "not found"})
		}
	case len(parts) == 3 && parts[0] == "boxes" && parts[2] == "runs" && r.Method == http.MethodPost:
		var req uno.RunRequest
		json.NewDecoder(r.Body).Decode(&req)
		run, err := m.SubmitRun(r.Context(), parts[1], req)
		if err != nil {
			write(404, map[string]string{"error": "not found"})
			return
		}
		write(200, map[string]any{"run": run})
	case len(parts) == 3 && parts[0] == "boxes" && parts[2] == "runs" && r.Method == http.MethodGet:
		m.mu.Lock()
		b, ok := m.boxes[parts[1]]
		m.mu.Unlock()
		if !ok {
			write(404, map[string]string{"error": "not found"})
			return
		}
		b.mu.Lock()
		runs := make([]*uno.Run, 0, len(b.runs))
		for _, rr := range b.runs {
			runs = append(runs, rr)
		}
		b.mu.Unlock()
		write(200, map[string]any{"runs": runs})
	case len(parts) == 4 && parts[0] == "boxes" && parts[2] == "runs" && r.Method == http.MethodGet:
		wait := 0
		fmt.Sscanf(r.URL.Query().Get("wait_s"), "%d", &wait)
		if run, ok := m.GetRun(parts[1], parts[3], wait); ok {
			write(200, map[string]any{"run": run})
		} else {
			write(404, map[string]string{"error": "not found"})
		}
	default:
		write(404, map[string]string{"error": "unknown route"})
	}
}
