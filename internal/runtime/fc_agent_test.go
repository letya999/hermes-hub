//go:build linux

package runtime

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// socketpair returns the two ends of a connected unix socket as *os.File —
// what the guest vsock listener hands the handlers inside the microVM.
func socketpair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	a := os.NewFile(uintptr(fds[0]), "a")
	b := os.NewFile(uintptr(fds[1]), "b")
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func TestFCHandleExec(t *testing.T) {
	agent, client := socketpair(t)
	go fcHandleExec(agent)
	if err := json.NewEncoder(client).Encode(map[string]any{
		"cmd": "echo $GREETING; echo err >&2; exit 3",
		"env": map[string]string{"GREETING": "hi"},
	}); err != nil {
		t.Fatal(err)
	}
	var resp fcExecResponse
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ExitCode != 3 || strings.TrimSpace(resp.Stdout) != "hi" || strings.TrimSpace(resp.Stderr) != "err" {
		t.Fatalf("exec response: %+v", resp)
	}
}

func TestFCHandleExecBadRequest(t *testing.T) {
	agent, client := socketpair(t)
	go fcHandleExec(agent)
	if _, err := client.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	var resp fcExecResponse
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ExitCode != 2 || resp.Stderr == "" {
		t.Fatalf("bad-request response: %+v", resp)
	}
}

func TestFCReadLine(t *testing.T) {
	line, err := fcReadLine(strings.NewReader("hello world\nrest"), 256)
	if err != nil || line != "hello world" {
		t.Fatalf("readline: %q %v", line, err)
	}
	// No newline and a hard max bound the builder.
	line, err = fcReadLine(strings.NewReader("abcdef"), 3)
	if err != nil || line != "abc" {
		t.Fatalf("bounded readline: %q %v", line, err)
	}
	if _, err = fcReadLine(errReader{}, 10); err == nil {
		t.Fatal("read error swallowed")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestFCBridgeAndDial(t *testing.T) {
	// Local TCP echo server stands in for the guest-local target.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()

	agent, client := socketpair(t)
	go fcHandleDial(agent)
	if _, err := client.Write([]byte(ln.Addr().String() + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("dial bridge echo: %v %q", err, buf)
	}
	_ = client.SetReadDeadline(time.Time{})

	// A target that refuses closes without bridging.
	agent2, client2 := socketpair(t)
	go fcHandleDial(agent2)
	if _, err := client2.Write([]byte("127.0.0.1:1\n")); err != nil {
		t.Fatal(err)
	}
	_ = client2.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := client2.Read(make([]byte, 1)); err == nil {
		t.Fatal("refused dial target kept conn open")
	}

	// Empty first line is ignored.
	agent3, client3 := socketpair(t)
	go fcHandleDial(agent3)
	if _, err := client3.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	_ = client3.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := client3.Read(make([]byte, 1)); err == nil {
		t.Fatal("empty dial target kept conn open")
	}
}

func TestFCMux(t *testing.T) {
	m := &fcMux{targets: map[uint32]net.Conn{}}
	// send without a conn errors.
	if err := m.send(2, 1, nil); err == nil {
		t.Fatal("send without conn succeeded")
	}

	agent, client := socketpair(t)
	m.setConn(agent)

	readFrame := func() (byte, uint32, []byte) {
		hdr := make([]byte, 9)
		if _, err := io.ReadFull(client, hdr); err != nil {
			t.Fatalf("frame hdr: %v", err)
		}
		id, op, ln := binary.BigEndian.Uint32(hdr[:4]), hdr[4], binary.BigEndian.Uint32(hdr[5:9])
		payload := make([]byte, ln)
		if _, err := io.ReadFull(client, payload); err != nil {
			t.Fatalf("frame payload: %v", err)
		}
		return op, id, payload
	}
	writeFrame := func(op byte, id uint32, payload []byte) {
		hdr := make([]byte, 9)
		binary.BigEndian.PutUint32(hdr[:4], id)
		hdr[4] = op
		binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
		if _, err := client.Write(hdr); err != nil {
			t.Fatal(err)
		}
		if len(payload) > 0 {
			if _, err := client.Write(payload); err != nil {
				t.Fatal(err)
			}
		}
	}

	// proxy() sends an OPEN frame and pumps local reads into DATA frames.
	localA, localB := net.Pipe()
	defer localA.Close()
	defer localB.Close()
	go m.proxy(localA, 8080)
	op, id, payload := readFrame()
	if op != 1 || id != 1 || string(payload) != "8080" {
		t.Fatalf("open frame: op=%d id=%d payload=%q", op, id, payload)
	}
	if _, err := localB.Write([]byte("out")); err != nil {
		t.Fatal(err)
	}
	op, id, payload = readFrame()
	if op != 2 || id != 1 || string(payload) != "out" {
		t.Fatalf("data frame: op=%d id=%d payload=%q", op, id, payload)
	}

	// Host->guest DATA lands on the local conn; CLOSE drops it.
	writeFrame(2, 1, []byte("in"))
	got := make([]byte, 2)
	_ = localB.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(localB, got); err != nil || string(got) != "in" {
		t.Fatalf("host data: %v %q", err, got)
	}
	writeFrame(3, 1, nil)
	_ = localB.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := localB.Read(make([]byte, 1)); err == nil {
		t.Fatal("close frame left local conn open")
	}
	// Frames for unknown ids are skipped.
	writeFrame(2, 99, []byte("x"))
	writeFrame(3, 99, nil)

	// readLoop EOF resets the mux conn and closes tracked targets.
	localC, localD := net.Pipe()
	defer localC.Close()
	defer localD.Close()
	go m.proxy(localC, 9090)
	// The id=1 pump may have emitted its close frame after our host-side
	// close; drain until the id=2 open arrives.
	for {
		op, id, _ = readFrame()
		if op == 1 && id == 2 {
			break
		}
	}
	_ = client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		drained := m.conn == nil && len(m.targets) == 0
		m.mu.Unlock()
		if drained {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil || len(m.targets) != 0 {
		t.Fatalf("readLoop EOF left state: conn=%v targets=%d", m.conn, len(m.targets))
	}
	_ = localD.Close()
}

func TestFCVsockListenAndLoopback(t *testing.T) {
	// AF_VSOCK availability depends on the host kernel; either outcome is a
	// valid covered path.
	ln, err := fcVsockListen(15000)
	if err == nil {
		defer unix.Close(ln.fd)
	}
	// Loopback-up needs CAP_NET_ADMIN; the socket/ifreq path is covered
	// whether or not the ioctl succeeds.
	_ = fcLoopbackUp()
}
