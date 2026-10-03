package main

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/mediasvc"
)

func TestNewServer(t *testing.T) {
	cfg := mediasvc.Config{
		Role: mediasvc.RoleSTT, Auth: "tok", DataDir: t.TempDir(),
		ListenAddr: "127.0.0.1:0", Workers: 1,
		Engine: "command", Command: "true", JobTTL: 0,
	}
	srv, err := newServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Addr != "127.0.0.1:0" || srv.Handler == nil {
		t.Fatalf("server: %+v", srv)
	}
	cfg.Engine = "nope"
	if _, err := newServer(cfg); err == nil {
		t.Fatal("unknown engine accepted")
	}
}
