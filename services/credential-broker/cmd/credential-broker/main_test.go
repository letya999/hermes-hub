package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/credential-broker/app"
)

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("writer closed") }
func TestCommands(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	if e := run(ctx, []string{"version"}, &out); e != nil || out.String() != version+"\n" {
		t.Fatal(e, out.String())
	}
	if e := run(ctx, []string{"version"}, failedWriter{}); e == nil {
		t.Fatal("writer error")
	}
	for _, args := range [][]string{nil, {"unknown"}, {"init"}, {"init", "--unknown"}, {"serve"}, {"serve", "--unknown"}, {"check-config", "--config", "relative"}} {
		if e := run(ctx, args, io.Discard); e == nil {
			t.Fatal(args)
		}
	}
	root := filepath.Join(t.TempDir(), "install")
	if e := run(ctx, []string{"init", "--dir", root}, io.Discard); e != nil {
		t.Fatal(e)
	}
	if e := run(ctx, []string{"init", "--dir", root}, io.Discard); e == nil {
		t.Fatal("reinit")
	}
	path := filepath.Join(root, "config.json")
	if e := run(ctx, []string{"check-config", "--config", path}, io.Discard); e != nil {
		t.Fatal(e)
	}
	cfg, e := app.ReadConfig(path)
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	cfg.Listen = ln.Addr().String()
	cfg.PublicOrigin = "http://" + cfg.Listen
	path2 := filepath.Join(root, "serve.json")
	if e = app.WriteJSON(path2, cfg); e != nil {
		t.Fatal(e)
	}
	if e = run(ctx, []string{"serve", "--config", path2}, io.Discard); e == nil {
		t.Fatal("busy port")
	}
	_ = ln.Close()
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if e = run(stopped, []string{"serve", "--config", path2}, io.Discard); e != nil {
		t.Fatal(e)
	}
	_ = os.Remove(cfg.MasterKeyFile)
	if e = run(ctx, []string{"serve", "--config", path2}, io.Discard); e == nil {
		t.Fatal("missing master")
	}
}
