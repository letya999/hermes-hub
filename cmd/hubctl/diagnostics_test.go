package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExportDiagnosticsIncludesOnlyHermesContainersAndReplacesSnapshot(t *testing.T) {
	root := t.TempDir()
	var calls []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "ps" {
			return []byte("other-app\nhermes-hub-alice-prod-toolhub-1\nhermes-context-123\n"), nil
		}
		return []byte("2026-09-26T12:00:00Z gateway telegram-message user=alice text=hello"), nil
	}
	for range 2 {
		path, err := exportDiagnostics(context.Background(), root, run)
		if err != nil || path != filepath.Join(root, ".local", "hermes-diagnostics.txt") {
			t.Fatal(path, err)
		}
		body, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(body), "telegram-message") || strings.Contains(string(body), "other-app") {
			t.Fatal(string(body), err)
		}
	}
	if len(calls) != 6 || strings.Contains(strings.Join(calls, "\n"), "logs --timestamps --tail 1000 other-app") {
		t.Fatal(calls)
	}
}

func TestExportDiagnosticsFailurePreservesLastSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".local", "hermes-diagnostics.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("previous snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, run := range []func(context.Context, ...string) ([]byte, error){
		func(context.Context, ...string) ([]byte, error) { return nil, errors.New("daemon stopped") },
		func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "ps" {
				return []byte("other-app\n"), nil
			}
			return nil, nil
		},
		func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "ps" {
				return []byte("hermes-hub-alice-prod-toolhub-1\n"), nil
			}
			return nil, errors.New("logs unavailable")
		},
	} {
		if _, err := exportDiagnostics(context.Background(), root, run); err == nil {
			t.Fatal("accepted incomplete diagnostic snapshot")
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "previous snapshot" {
			t.Fatal(string(body), err)
		}
	}
}

func TestExportDiagnosticsRejectsNonDirectoryOutputRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".local"), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(context.Context, ...string) ([]byte, error) {
		return []byte("hermes-hub-alice-prod-runtime-1\n"), nil
	}
	if _, err := exportDiagnostics(context.Background(), root, run); err == nil {
		t.Fatal("overwrote unexpected .local file")
	}
	if body, err := os.ReadFile(filepath.Join(root, ".local")); err != nil || string(body) != "occupied" {
		t.Fatal(string(body), err)
	}
}

func TestLogsExportCommandUsesLocalPrivateSnapshot(t *testing.T) {
	bin := t.TempDir()
	name := "docker"
	script := "#!/bin/sh\ncase \"$1\" in ps) echo hermes-hub-alice-prod-runtime-1 ;; logs) echo accepted-message ;; esac\n"
	if runtime.GOOS == "windows" {
		name = "docker.cmd"
		script = "@echo off\r\nif \"%1\"==\"ps\" echo hermes-hub-alice-prod-runtime-1\r\nif \"%1\"==\"logs\" echo accepted-message\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	if err := run(context.Background(), []string{"logs-export", "--root", root}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".local", "hermes-diagnostics.txt"))
	if err != nil || !strings.Contains(string(body), "accepted-message") {
		t.Fatal(string(body), err)
	}
}
