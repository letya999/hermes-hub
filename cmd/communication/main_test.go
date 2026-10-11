package main

import (
	"bytes"
	"encoding/base64"
	"errors"
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
