package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestM10ScriptHelperInputAndOutput(t *testing.T) {
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	due := time.Now().UTC().Format(time.RFC3339Nano)
	var out bytes.Buffer
	if err := runScriptHelper([]string{encode(`emit("ok")`), due}, &out); err != nil || out.String() != "[\"ok\"]\n" {
		t.Fatalf("helper=%q %v", out.String(), err)
	}
	for _, args := range [][]string{nil, {"bad", due}, {encode(`emit("ok")`), "not a date"}, {encode(`load("file", "x")`), due}, {encode(`emit("x" * 5000)`), due}} {
		if err := runScriptHelper(args, &out); err == nil {
			t.Fatalf("invalid input accepted: %v", args)
		}
	}
	if err := runScriptHelper([]string{encode(`emit("ok")`), due}, failingWriter{}); err == nil {
		t.Fatal("output failure reported success")
	}
}

func TestScriptHelperProbeInputAndWatch(t *testing.T) {
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	due := time.Now().UTC().Format(time.RFC3339Nano)
	var out bytes.Buffer
	if err := runScriptHelper([]string{encode(`emit(input())`), due, encode("fetched-body"), "-"}, &out); err != nil || out.String() != "[\"fetched-body\"]\n" {
		t.Fatalf("probe input helper=%q %v", out.String(), err)
	}
	watchDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(watchDir, "state.txt"), []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runScriptHelper([]string{encode(`emit(read_file("state.txt"))`), due, encode(""), watchDir}, &out); err != nil || out.String() != "[\"v1\"]\n" {
		t.Fatalf("watch helper=%q %v", out.String(), err)
	}
	out.Reset()
	if err := runScriptHelper([]string{encode(`emit(read_file("state.txt"))`), due, encode(""), "-"}, &out); err == nil {
		t.Fatal("file builtins must not exist without the watch mount")
	}
}

func TestScriptHelperNeeds(t *testing.T) {
	encode := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	due := time.Now().UTC().Format(time.RFC3339Nano)
	var out bytes.Buffer
	if err := runScriptHelper([]string{encode(`emit(needs()[0] + "|" + needs()[1])`), due, encode(""), "-", encode("alpha"), encode("beta")}, &out); err != nil || out.String() != "[\"alpha|beta\"]\n" {
		t.Fatalf("needs helper=%q %v", out.String(), err)
	}
	out.Reset()
	if err := runScriptHelper([]string{encode(`emit("ok")`), due, encode(""), "-", encode("a"), encode("b"), encode("c"), encode("d"), encode("e")}, &out); err == nil {
		t.Fatal("more than four needs accepted")
	}
}
