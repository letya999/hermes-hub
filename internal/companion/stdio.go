package companion

import (
	"context"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioMCPTransport runs a newline JSON-RPC MCP over a child process.
// Go SDK v1.7 first sends server/discover (protocol 2026-07-28). Servers that
// neither implement it nor return JSON-RPC method-not-found hang the handshake
// (rust-mcp-filesystem). Swallow discover and synthesize -32601 so Connect
// falls back to initialize.
func stdioMCPTransport(cmd *exec.Cmd) mcp.Transport {
	return discoverFallbackTransport{inner: &mcp.CommandTransport{Command: cmd}}
}

type discoverFallbackTransport struct {
	inner mcp.Transport
}

func (t discoverFallbackTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := &discoverFallbackConn{inner: conn, out: make(chan msgOrErr, 8)}
	go wrapped.pump()
	return wrapped, nil
}

type msgOrErr struct {
	msg jsonrpc.Message
	err error
}

type discoverFallbackConn struct {
	inner mcp.Connection
	out   chan msgOrErr
}

func (c *discoverFallbackConn) pump() {
	for {
		msg, err := c.inner.Read(context.Background())
		c.out <- msgOrErr{msg: msg, err: err}
		if err != nil {
			return
		}
	}
}

func (c *discoverFallbackConn) SessionID() string { return c.inner.SessionID() }

func (c *discoverFallbackConn) Close() error { return c.inner.Close() }

func (c *discoverFallbackConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if req, ok := msg.(*jsonrpc.Request); ok && req.Method == "server/discover" {
		c.out <- msgOrErr{msg: &jsonrpc.Response{
			ID:    req.ID,
			Error: &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "Method not found"},
		}}
		return nil
	}
	return c.inner.Write(ctx, msg)
}

func (c *discoverFallbackConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case got := <-c.out:
		return got.msg, got.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
