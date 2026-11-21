//go:build linux

package hermes

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// fc-agent runs inside the Firecracker guest as PID 1's service set:
//   - vsock :5000 exec service (one JSON request/response per connection)
//   - vsock :5001 dial service (first line "host:port\n" then bridged to
//     that address on the guest loopback)
//   - vsock :5002 mux service: the host opens one persistent connection and
//     the agent pushes OPEN/DATA/CLOSE frames for guest-local proxy
//     listeners (HUB_FC_PROXIES, comma-separated TCP ports). The host side
//     owns the egress allowlist; the guest cannot dial out any other way.
type fcExecRequest struct {
	Cmd      string            `json:"cmd"`
	Env      map[string]string `json:"env,omitempty"`
	TimeoutS int               `json:"timeout_s,omitempty"`
}

type fcExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// vsockLn is a raw-fd vsock listener: the stdlib net package cannot wrap
// AF_VSOCK sockets, so accept returns *os.File streams directly.
type vsockLn struct{ fd int }

func fcVsockListen(port uint32) (*vsockLn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		unix.Close(fd) // #nosec G104 -- best-effort; error intentionally ignored
		return nil, fmt.Errorf("vsock bind :%d: %w", port, err)
	}
	if err := unix.Listen(fd, 64); err != nil {
		unix.Close(fd) // #nosec G104 -- best-effort; error intentionally ignored
		return nil, fmt.Errorf("vsock listen: %w", err)
	}
	return &vsockLn{fd: fd}, nil
}

func (l *vsockLn) Accept() (*os.File, error) {
	nfd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(nfd), "vsock-conn"), nil
}

func fcBridge(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }() // #nosec G104 -- copy errors end the bridge anyway
	go func() { io.Copy(b, a); done <- struct{}{} }() // #nosec G104 -- copy errors end the bridge anyway
	<-done
	a.Close() // #nosec G104 -- best-effort; error intentionally ignored
	b.Close() // #nosec G104 -- best-effort; error intentionally ignored
}

func fcHandleExec(conn *os.File) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Minute)) // #nosec G104 -- best-effort; error intentionally ignored
	var req fcExecRequest
	if err := json.NewDecoder(io.LimitReader(conn, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Cmd) == "" {
		json.NewEncoder(conn).Encode(fcExecResponse{ExitCode: 2, Stderr: "bad exec request"}) // #nosec G104 -- best-effort; error intentionally ignored
		return
	}
	timeout := time.Duration(req.TimeoutS) * time.Second
	if timeout <= 0 || timeout > 4*time.Minute {
		timeout = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", req.Cmd)
	env := os.Environ()
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		code = 1
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		}
	}
	json.NewEncoder(conn).Encode(fcExecResponse{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}) // #nosec G104 -- best-effort; error intentionally ignored
}

func fcHandleDial(conn *os.File) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second)) // #nosec G104 -- best-effort; error intentionally ignored
	line, err := fcReadLine(conn, 256)
	if err != nil || strings.TrimSpace(line) == "" {
		return
	}
	target, err := net.DialTimeout("tcp", strings.TrimSpace(line), 5*time.Second)
	if err != nil {
		return
	}
	conn.SetDeadline(time.Time{})   // #nosec G104 -- best-effort; error intentionally ignored
	target.SetDeadline(time.Time{}) // #nosec G104 -- best-effort; error intentionally ignored
	fcBridge(conn, target)
}

func fcReadLine(r io.Reader, max int) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < max {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// ---- mux: guest-local TCP -> frames on the host-owned vsock conn ----

// frame: [4B connID][1B op][4B len][payload]; ops 1=open 2=data 3=close.

type fcMux struct {
	mu      sync.Mutex
	conn    io.WriteCloser
	nextID  uint32
	targets map[uint32]net.Conn
}

func (m *fcMux) setConn(c *os.File) {
	m.mu.Lock()
	m.conn = c
	m.mu.Unlock()
	go m.readLoop(c)
}

func (m *fcMux) send(op byte, id uint32, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return errors.New("no mux conn")
	}
	hdr := make([]byte, 9)
	binary.BigEndian.PutUint32(hdr[:4], id)
	hdr[4] = op
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload))) // #nosec G115 -- mux frames carry bounded control payloads, far below 4 GiB
	if _, err := m.conn.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := m.conn.Write(payload)
		return err
	}
	return nil
}

func (m *fcMux) readLoop(c *os.File) {
	hdr := make([]byte, 9)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			m.mu.Lock()
			if m.conn == c {
				m.conn = nil
			}
			for id, tc := range m.targets {
				delete(m.targets, id)
				tc.Close() // #nosec G104 -- best-effort; error intentionally ignored
			}
			m.mu.Unlock()
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
		m.mu.Lock()
		tc := m.targets[id]
		if op == 3 && tc != nil {
			delete(m.targets, id)
		}
		m.mu.Unlock()
		if tc == nil {
			continue
		}
		switch op {
		case 2:
			tc.Write(payload) // #nosec G104 -- best-effort; error intentionally ignored
		case 3:
			tc.Close() // #nosec G104 -- best-effort; error intentionally ignored
		}
	}
}

func (m *fcMux) proxy(local net.Conn, guestPort int) {
	m.mu.Lock()
	m.nextID++
	id := m.nextID
	m.mu.Unlock()
	if err := m.send(1, id, []byte(fmt.Sprintf("%d", guestPort))); err != nil {
		local.Close() // #nosec G104 -- best-effort; error intentionally ignored
		return
	}
	m.mu.Lock()
	m.targets[id] = local
	m.mu.Unlock()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := local.Read(buf)
			if n > 0 {
				if err := m.send(2, id, buf[:n]); err != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		m.send(3, id, nil) // #nosec G104 -- best-effort; error intentionally ignored
		m.mu.Lock()
		if m.targets[id] == local {
			delete(m.targets, id)
		}
		m.mu.Unlock()
		local.Close() // #nosec G104 -- best-effort; error intentionally ignored
	}()
}

// fcLoopbackUp brings `lo` up — kernel boots with it down and every
// guest-local dial (runtime API, gateway health, proxies) would fail with
// ENETUNREACH without it.
func fcLoopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("lo socket: %w", err)
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return fmt.Errorf("lo ifreq: %w", err)
	}
	ifr.SetUint32(uint32(unix.IFF_UP | unix.IFF_RUNNING))
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("lo up: %w", err)
	}
	return nil
}

func runFCAgent(_ context.Context) error {
	if err := fcLoopbackUp(); err != nil {
		return err
	}
	execLn, err := fcVsockListen(5000)
	if err != nil {
		return err
	}
	dialLn, err := fcVsockListen(5001)
	if err != nil {
		return err
	}
	muxLn, err := fcVsockListen(5002)
	if err != nil {
		return err
	}
	// Ingress ports: "CONNECT <port>" on the vsock UDS lands on a guest vsock
	// listener; bridge it to the same TCP port on guest loopback so host-side
	// relays reach guest services without a guest NIC.
	ingress := os.Getenv("HUB_FC_INGRESS")
	if cmdline, err := os.ReadFile("/proc/cmdline"); err == nil {
		for _, field := range strings.Fields(string(cmdline)) {
			if v, ok := strings.CutPrefix(field, "fc.ingress="); ok {
				ingress = v
			}
		}
	}
	for _, p := range strings.Split(ingress, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var port uint32
		if _, err := fmt.Sscanf(p, "%d", &port); err != nil || port == 0 {
			return fmt.Errorf("fc ingress port %q invalid", p)
		}
		ln, err := fcVsockListen(port)
		if err != nil {
			return err
		}
		go func(ln *vsockLn, port uint32) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					target, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
					if err != nil {
						c.Close() // #nosec G104 -- best-effort; error intentionally ignored
						return
					}
					fcBridge(c, target)
				}()
			}
		}(ln, port)
	}
	mux := &fcMux{targets: map[uint32]net.Conn{}}
	go func() {
		for {
			c, err := execLn.Accept()
			if err != nil {
				return
			}
			go fcHandleExec(c)
		}
	}()
	go func() {
		for {
			c, err := dialLn.Accept()
			if err != nil {
				return
			}
			go fcHandleDial(c)
		}
	}()
	go func() {
		for {
			c, err := muxLn.Accept()
			if err != nil {
				return
			}
			mux.setConn(c)
		}
	}()
	proxies := os.Getenv("HUB_FC_PROXIES")
	if cmdline, err := os.ReadFile("/proc/cmdline"); err == nil {
		for _, field := range strings.Fields(string(cmdline)) {
			if v, ok := strings.CutPrefix(field, "fc.proxies="); ok {
				proxies = v
			}
		}
	}
	for _, p := range strings.Split(proxies, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ln, err := net.Listen("tcp", "127.0.0.1:"+p)
		if err != nil {
			return fmt.Errorf("fc proxy listen :%s: %w", p, err)
		}
		var port int
		fmt.Sscanf(p, "%d", &port) // #nosec G104 -- best-effort; error intentionally ignored
		go func(ln net.Listener, port int) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go mux.proxy(c, port)
			}
		}(ln, port)
	}
	fmt.Fprintln(os.Stderr, "fc-agent: ready (exec :5000 dial :5001 mux :5002)")
	select {}
}
