package companion

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProcNetListenPorts(t *testing.T) {
	fixture := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 11111 1 0000000000000000 100 0 0 10 0
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22222 1 0000000000000000 100 0 0 10 0
   2: AC100002:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 33333 1 0000000000000000 100 0 0 10 0
   3: 0100007F:270F 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 44444 1 0000000000000000 100 0 0 10 0
   4: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 55555 1 0000000000000000 100 0 0 10 0
`
	path := filepath.Join(t.TempDir(), "tcp")
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}
	// 5432 loopback, 8080 wildcard; the 172.16-bound and TIME_WAIT rows and the
	// duplicate are skipped.
	got := procNetListenPorts(path, nil)
	if len(got) != 2 || got[0] != 5432 || got[1] != 8080 {
		t.Fatalf("ports = %v", got)
	}
	if excluded := procNetListenPorts(path, map[int]bool{5432: true}); len(excluded) != 1 || excluded[0] != 8080 {
		t.Fatalf("excluded ports = %v", excluded)
	}
}

func TestProcNetListenPortsV6(t *testing.T) {
	fixture := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:2328 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11111 1
   1: 00000000000000000000000000000001:270F 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22222 1
   2: 0000000000000000FFFF0000AC100002:0050 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 33333 1
`
	path := filepath.Join(t.TempDir(), "tcp6")
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}
	// :: wildcard (9000) and ::1 (9999) count; the 172.16-mapped bind does not.
	got := procNetListenPorts(path, nil)
	if len(got) != 2 || got[0] != 9000 || got[1] != 9999 {
		t.Fatalf("ports = %v", got)
	}
}

func TestLoopbackOrWildcardHex(t *testing.T) {
	for hexAddr, want := range map[string]bool{
		"0100007F": true, "00000000": true, "AC100002": false,
		"00000000000000000000000000000000": true, "00000000000000000000000000000001": true,
		"0000000000000000FFFF0000AC100002": false, "0100007": false, "": false,
	} {
		if got := loopbackOrWildcardHex(hexAddr); got != want {
			t.Fatalf("loopbackOrWildcardHex(%q) = %v, want %v", hexAddr, got, want)
		}
	}
}

func TestCandidateEndpointPaths(t *testing.T) {
	if got := candidateEndpointPaths("/events"); len(got) != 1 || got[0] != "/events" {
		t.Fatalf("paths = %v", got)
	}
	if got := candidateEndpointPaths(""); len(got) != 4 || got[0] != "/sse" {
		t.Fatalf("default paths = %v", got)
	}
}

func TestNetworkTransportsKindOrdering(t *testing.T) {
	if got := networkTransports("sse", 13080, "/sse"); len(got) != 1 {
		t.Fatalf("sse transports = %v", got)
	}
	if got := networkTransports("streamable-http", 3000, "/mcp"); len(got) != 1 {
		t.Fatalf("streamable transports = %v", got)
	}
	// "auto" and "" try legacy SSE first, then streamable HTTP.
	for _, kind := range []string{"", "auto"} {
		got := networkTransports(kind, 13080, "/sse")
		if len(got) != 2 {
			t.Fatalf("kind %q transports = %v", kind, got)
		}
	}
}

func TestCandidateListenPortsDeclared(t *testing.T) {
	if got := candidateListenPorts(13080, nil); len(got) != 1 || got[0] != 13080 {
		t.Fatalf("declared ports = %v", got)
	}
}

// fakeBridgeConn is a scripted mcp.Connection for pumpBridge tests.
type fakeBridgeConn struct {
	in      chan jsonrpc.Message
	written []jsonrpc.Message
	mu      sync.Mutex
}

func (f *fakeBridgeConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case msg, ok := <-f.in:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeBridgeConn) Write(_ context.Context, msg jsonrpc.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, msg)
	return nil
}

func (f *fakeBridgeConn) Close() error      { return nil }
func (f *fakeBridgeConn) SessionID() string { return "s" }

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestPumpBridgeBothDirections(t *testing.T) {
	conn := &fakeBridgeConn{in: make(chan jsonrpc.Message, 2)}
	resp, err := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	conn.in <- resp
	pr, pw := io.Pipe()
	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pumpBridge(ctx, conn, pr, &out) }()
	if _, err := pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn.mu.Lock()
		n := len(conn.written)
		conn.mu.Unlock()
		if n > 0 && strings.Contains(out.String(), `"ok":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pump did not relay: written=%d out=%q", n, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = pw.Close()
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("pump = %v", err)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.written[0].(*jsonrpc.Request).Method != "initialize" {
		t.Fatalf("written = %v", conn.written)
	}
}

func TestPumpBridgeRejectsNonJSONRPC(t *testing.T) {
	conn := &fakeBridgeConn{in: make(chan jsonrpc.Message)}
	var out bytes.Buffer
	err := pumpBridge(context.Background(), conn, strings.NewReader("not json\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "JSON-RPC") {
		t.Fatalf("pump = %v", err)
	}
}

func TestConnectNetworkTimeoutWhenNoEndpointAnswers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = connectNetwork(ctx, networkProbe{Kind: "sse", Port: port, Path: "/sse", Wait: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("closed endpoint connected")
	}
}

// TestConnectNetworkLiveSSE drives the real client path: an SSE MCP server on
// loopback, discovered endpoint, transport connect, and a full client
// initialize handshake — the same sequence the workload companion runs.
func TestConnectNetworkLiveSSE(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	mux := http.NewServeMux()
	mux.Handle("/sse", mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	port, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Skipf("non-loopback test server %q: %v", srv.URL, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := connectNetwork(ctx, networkProbe{Kind: "sse", Port: port, Path: "/sse", Wait: 3 * time.Second})
	if err != nil {
		t.Fatalf("connectNetwork: %v", err)
	}
	defer conn.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "1"}, nil)
	session, err := client.Connect(ctx, &staticTransport{conn: conn}, nil)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer session.Close()
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_BRIDGE_HELPER") != "1" {
		return
	}
	os.Exit(0)
}

func TestConnectNetworkChildEarlyExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_BRIDGE_HELPER=1")
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := connectNetworkChild(ctx, client, cmd, Config{Transport: "sse"})
	if err == nil {
		t.Fatal("dead child produced a session")
	}
}
