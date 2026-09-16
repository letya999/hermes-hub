package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalConnectorPreparation(t *testing.T) {
	root := t.TempDir()
	registry := filepath.Join(root, "runtime", "toolhub", "store.json")
	store := filepath.Join(root, "runtime", "credentials", "store.enc")
	key := filepath.Join(root, "keys", "cipher.key")
	token := filepath.Join(root, "keys", "local.key")
	args := []string{"prepare", "--user", "alice", "--toolhub-store", registry, "--store", store, "--key-file", key, "--local-token-file", token}
	if err := runConnector(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(token)
	if err != nil || len(before) != 64 {
		t.Fatal(err)
	}
	if err := runConnector(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(token)
	if string(before) != string(after) {
		t.Fatal("token replaced")
	}
	args[len(args)-1] = key
	if runConnector(context.Background(), args) == nil {
		t.Fatal("cipher key reused as service token")
	}
	args[len(args)-1] = "relative"
	if runConnector(context.Background(), args) == nil {
		t.Fatal("relative token path")
	}
	for _, args := range [][]string{{"local-controller"}, {"local-controller", "--config", "relative"}, {"local-controller", "--unknown"}, {"local-controller", "--config", filepath.Join(root, "missing")}, {"local-controller", "--config", registry}, {"local-controller", "--config", registry, "extra"}} {
		if runConnector(context.Background(), args) == nil {
			t.Fatal(args)
		}
	}
	config := filepath.Join(root, "controller.json")
	if err := os.WriteFile(config, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runConnector(context.Background(), []string{"local-controller", "--config", config, "--token-file", token}); err == nil || !strings.Contains(err.Error(), "invalid local controller") {
		t.Fatal(err)
	}
	if runConnector(context.Background(), []string{"local-controller", "--config", config, "--token-file", filepath.Join(root, "missing")}) == nil {
		t.Fatal("missing token accepted")
	}
}
