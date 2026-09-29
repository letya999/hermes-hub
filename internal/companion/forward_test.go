package companion

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestStartTCPForwardsValidation(t *testing.T) {
	ctx := context.Background()
	for _, forwards := range [][]TCPForwardConfig{
		{{Listen: 80, TargetEnv: "DB_ENDPOINT"}},
		{{Listen: 70000, TargetEnv: "DB_ENDPOINT"}},
		{{Listen: 15432, TargetEnv: "db_endpoint"}},
		{{Listen: 15432, TargetEnv: ""}},
		{{Listen: 15432, TargetEnv: "DB_ENDPOINT"}, {Listen: 15432, TargetEnv: "DB2_ENDPOINT"}},
		{{Listen: 15432, TargetEnv: "DB_ENDPOINT"}, {Listen: 15433, TargetEnv: "DB_ENDPOINT"}},
	} {
		if err := startTCPForwards(ctx, forwards); err == nil {
			t.Fatalf("invalid tcp forwards accepted: %+v", forwards)
		}
	}
	// Unset targets leave the forward inactive instead of failing the start.
	if err := startTCPForwards(ctx, []TCPForwardConfig{{Listen: freeForwardPort(t), TargetEnv: "MISSING_ENDPOINT_ENV"}}); err != nil {
		t.Fatalf("unset forward target must be skipped: %v", err)
	}
}

func TestTCPForwardRequiresProxy(t *testing.T) {
	t.Setenv("FORWARD_ENDPOINT", "db.example.com:5432")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	err := startTCPForwards(context.Background(), []TCPForwardConfig{{Listen: freeForwardPort(t), TargetEnv: "FORWARD_ENDPOINT"}})
	if err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Fatalf("forward without a proxy must fail closed: %v", err)
	}
}

func TestTCPForwardRejectsMalformedTarget(t *testing.T) {
	t.Setenv("FORWARD_ENDPOINT", "not-a-socket")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	err := startTCPForwards(context.Background(), []TCPForwardConfig{{Listen: freeForwardPort(t), TargetEnv: "FORWARD_ENDPOINT"}})
	if err == nil {
		t.Fatal("malformed forward target accepted")
	}
}

// TestTCPForwardRelaysThroughConnectProxy stands up a stub CONNECT proxy and
// an echo target, then checks a byte makes the whole loopback -> CONNECT ->
// target -> back trip.
func TestTCPForwardRelaysThroughConnectProxy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	var connectTo string
	proxy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	go func() {
		for {
			conn, err := proxy.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				line, err := reader.ReadString('\n')
				if err != nil || !strings.HasPrefix(line, "CONNECT ") {
					return
				}
				connectTo = strings.TrimSpace(strings.TrimPrefix(line, "CONNECT "))
				connectTo = strings.TrimSuffix(strings.TrimSuffix(connectTo, "HTTP/1.1"), " ")
				for {
					header, err := reader.ReadString('\n')
					if err != nil || header == "\r\n" {
						break
					}
				}
				upstream, err := net.DialTimeout("tcp4", target.Addr().String(), 2*time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(upstream, reader); done <- struct{}{} }()
				go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
				<-done
			}()
		}
	}()

	listen := freeForwardPort(t)
	t.Setenv("FORWARD_ENDPOINT", target.Addr().String())
	t.Setenv("HTTP_PROXY", "http://"+proxy.Addr().String())
	t.Setenv("HTTPS_PROXY", "")
	if err := startTCPForwards(ctx, []TCPForwardConfig{{Listen: listen, TargetEnv: "FORWARD_ENDPOINT"}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", listen), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("unexpected relay payload %q", buf)
	}
	if connectTo != target.Addr().String()+" " && !strings.HasPrefix(connectTo, target.Addr().String()) {
		t.Fatalf("proxy was asked for %q", connectTo)
	}
}

func freeForwardPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
