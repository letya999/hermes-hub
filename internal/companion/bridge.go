package companion

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunBridge is the preflight half of the network-transport fallback. The
// workload-side companion re-exposes tools over HTTP, but the isolated
// tools/list probe needs plain stdin/stdout: the artifact container runs the
// real server, and this process is attached to its network namespace
// (--network container:<server>), discovers the loopback listener and pumps
// newline JSON-RPC between stdin/stdout and the discovered MCP endpoint.
//
// The relay is deliberately dumb: it forwards valid JSON-RPC messages in both
// directions so initialize, notifications and tools/list flow untouched.
func RunBridge(ctx context.Context, wait time.Duration, kind, path string, port int) error {
	conn, err := connectNetwork(ctx, networkProbe{Kind: kind, Path: path, Port: port, Wait: wait})
	if err != nil {
		return err
	}
	defer conn.Close()
	fmt.Fprintf(os.Stderr, "mcp-bridge connected (%s)\n", conn.SessionID())
	return pumpBridge(ctx, conn, os.Stdin, os.Stdout)
}

// pumpBridge relays newline JSON-RPC between in/out and the network conn in
// both directions until either side ends. Non-JSON-RPC input is rejected
// rather than forwarded as noise.
func pumpBridge(ctx context.Context, conn mcp.Connection, in io.Reader, out io.Writer) error {
	var writeMu sync.Mutex
	writer := bufio.NewWriterSize(out, 1<<20)
	writeStdout := func(msg jsonrpc.Message) error {
		body, err := jsonrpc.EncodeMessage(msg)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := writer.Write(body); err != nil {
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			return err
		}
		return writer.Flush()
	}
	done := make(chan error, 2)
	// conn -> out
	go func() {
		for {
			msg, err := conn.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			if err := writeStdout(msg); err != nil {
				done <- err
				return
			}
		}
	}()
	// in -> conn
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			msg, err := jsonrpc.DecodeMessage(append(json.RawMessage(nil), line...))
			if err != nil {
				done <- fmt.Errorf("input is not a JSON-RPC message: %w", err)
				return
			}
			if err := conn.Write(ctx, msg); err != nil {
				done <- err
				return
			}
		}
		done <- io.EOF
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
