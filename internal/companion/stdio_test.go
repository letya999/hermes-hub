package companion

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeConn struct {
	reads  []jsonrpc.Message
	writes []jsonrpc.Message
	closed bool
	err    error
}

func (f *fakeConn) SessionID() string { return "sess-1" }

func (f *fakeConn) Close() error { f.closed = true; return nil }

func (f *fakeConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.reads) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	msg := f.reads[0]
	f.reads = f.reads[1:]
	return msg, nil
}

func (f *fakeConn) Write(_ context.Context, msg jsonrpc.Message) error {
	f.writes = append(f.writes, msg)
	return nil
}

type fakeTransport struct {
	conn mcp.Connection
	err  error
}

func (f fakeTransport) Connect(context.Context) (mcp.Connection, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.conn, nil
}

func TestDiscoverFallbackConnectError(t *testing.T) {
	_, err := (discoverFallbackTransport{inner: fakeTransport{err: errors.New("boom")}}).Connect(context.Background())
	if err == nil {
		t.Fatal("expected connect error")
	}
}

func TestDiscoverFallbackSessionIDAndClose(t *testing.T) {
	inner := &fakeConn{}
	conn, err := (discoverFallbackTransport{inner: fakeTransport{conn: inner}}).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if conn.SessionID() != "sess-1" {
		t.Fatalf("session %q", conn.SessionID())
	}
	if err := conn.Close(); err != nil || !inner.closed {
		t.Fatalf("close: err=%v closed=%v", err, inner.closed)
	}
}

func TestDiscoverFallbackWritePassthroughAndReadCancel(t *testing.T) {
	inner := &fakeConn{}
	conn, err := (discoverFallbackTransport{inner: fakeTransport{conn: inner}}).Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := &jsonrpc.Request{Method: "initialize"}
	if err := conn.Write(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(inner.writes) != 1 {
		t.Fatalf("writes %d", len(inner.writes))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := conn.Read(ctx); err == nil {
		t.Fatal("expected canceled read")
	}
}

func TestStdioMCPTransportWrapsCommand(t *testing.T) {
	if stdioMCPTransport(exec.Command("true")) == nil {
		t.Fatal("nil transport")
	}
}
