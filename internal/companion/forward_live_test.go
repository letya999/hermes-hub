package companion

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveForwardThroughProxy exercises the forwarder against a real HTTP
// CONNECT proxy and a real PostgreSQL listener. It is skipped unless
// HERMES_LIVE_FORWARD is set. Expected environment:
//
//	HERMES_LIVE_FORWARD=1
//	LIVE_FORWARD_PROXY=http://127.0.0.1:3128   (squid permitting target)
//	LIVE_FORWARD_DB_ENDPOINT=chg55-pg:5432      (CONNECT target host:port)
//	LIVE_DBHUB_DIR=<path to a built bytebase/dbhub checkout> (optional)
//	LIVE_DBHUB_DSN_SUFFIX=/testdb?sslmode=disable (optional, default below)
func TestLiveForwardThroughProxy(t *testing.T) {
	if os.Getenv("HERMES_LIVE_FORWARD") == "" {
		t.Skip("live forward test disabled; set HERMES_LIVE_FORWARD=1")
	}
	target := os.Getenv("LIVE_FORWARD_DB_ENDPOINT")
	if target == "" {
		t.Skip("LIVE_FORWARD_DB_ENDPOINT unset")
	}
	t.Setenv("HTTP_PROXY", os.Getenv("LIVE_FORWARD_PROXY"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := startTCPForwards(ctx, []TCPForwardConfig{{Listen: 15432, TargetEnv: "LIVE_FORWARD_DB_ENDPOINT"}}); err != nil {
		t.Fatalf("startTCPForwards: %v", err)
	}

	// A raw PostgreSQL StartupMessage through forward -> CONNECT -> target must
	// draw a protocol response (R auth request or E/F error), proving wire-level
	// relay, not just a TCP handshake.
	conn, err := net.DialTimeout("tcp4", "127.0.0.1:15432", 10*time.Second)
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	body := append([]byte("user\x00ro\x00database\x00testdb\x00"), 0)
	msg := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(msg[:4], uint32(len(msg)))
	binary.BigEndian.PutUint32(msg[4:8], 196608)
	copy(msg[8:], body)
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write startup: %v", err)
	}
	head := make([]byte, 5)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("read startup response: %v", err)
	}
	if typ := head[0]; typ != 'R' && typ != 'E' {
		t.Fatalf("unexpected postgres response byte %q", typ)
	}
	t.Logf("forward->CONNECT->postgres replied message type %q", head[0])

	dbhubDir := os.Getenv("LIVE_DBHUB_DIR")
	if dbhubDir == "" {
		return
	}
	suffix := os.Getenv("LIVE_DBHUB_DSN_SUFFIX")
	if suffix == "" {
		suffix = "/testdb?sslmode=disable"
	}
	cmd := exec.Command("node", filepath.Join(os.TempDir(), "mcp-stdio-probe.cjs"), "node", filepath.Join(dbhubDir, "dist", "index.js"))
	cmd.Dir = dbhubDir
	cmd.Env = append(os.Environ(),
		"DSN=postgres://ro:ropass@127.0.0.1:15432"+suffix,
		`MCP_CALL_JSON={"name":"execute_sql","arguments":{"sql":"SELECT 1 AS one, current_user AS u"}}`,
	)
	out, err := cmd.CombinedOutput()
	text := string(out)
	// The tool payload arrives JSON-escaped inside the MCP envelope.
	if err != nil || !strings.Contains(text, `CALL_RESULT`) || !strings.Contains(text, `\"one\": 1`) || !strings.Contains(text, `\"u\": \"ro\"`) {
		t.Fatalf("dbhub through forward failed: %v\n%s", err, text)
	}
	if !strings.Contains(text, "INIT_OK") || !strings.Contains(text, `"execute_sql"`) {
		t.Fatalf("dbhub handshake/tools incomplete:\n%s", text)
	}
	t.Log("dbhub SELECT through companion forward + CONNECT proxy succeeded")

	txttsqlBin := os.Getenv("LIVE_TXTTSQL_BIN")
	if txttsqlBin == "" {
		return
	}
	sources := `[{"id":"live","kind":"postgres","host":"127.0.0.1","port":15432,"database":"testdb","user":"ro","password":{"kind":"env","name":"LIVE_TXTTSQL_PASS"},"max_rows":10}]`
	cmd = exec.Command("node", filepath.Join(os.TempDir(), "mcp-stdio-probe.cjs"), txttsqlBin, "--config", filepath.Join(os.TempDir(), "txttsql-missing.toml"))
	cmd.Env = append(os.Environ(),
		"TXTTSQL_SOURCES="+sources,
		"LIVE_TXTTSQL_PASS=ropass",
		`MCP_CALL_JSON={"name":"execute_sql","arguments":{"source":"live","sql":"SELECT 1 AS one, current_user AS u"}}`,
	)
	out, err = cmd.CombinedOutput()
	text = string(out)
	if err != nil || !strings.Contains(text, `INIT_OK`) || !strings.Contains(text, `CALL_RESULT`) ||
		strings.Contains(text, `"isError":true`) || strings.Contains(text, `"isError": true`) {
		t.Fatalf("txttsql through forward failed: %v\n%s", err, text)
	}
	t.Log("txttsql SELECT through companion forward + CONNECT proxy succeeded")
}
