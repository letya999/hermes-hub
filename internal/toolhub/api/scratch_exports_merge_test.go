package api

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestApplyExportsContract(t *testing.T) {
	ctx := context.Background()
	eff := EffectiveBinding{WorkloadID: "wl-1"}
	if _, _, err := ApplyExports(ctx, nil, nil, eff, AgentExecRequest{}, "p", ""); err != nil {
		t.Fatal("empty export")
	}
	// exports re-check authority; a missing or denying fence stops them
	if _, _, err := ApplyExports(ctx, nil, nil, eff, AgentExecRequest{}, "p", "eA=="); !errors.Is(err, ErrIsolation) {
		t.Fatalf("no fence: %v", err)
	}
	deny := func(context.Context, EffectiveBinding) error { return errors.New("not yours") }
	if _, _, err := ApplyExports(ctx, nil, deny, eff, AgentExecRequest{}, "p", "eA=="); err == nil {
		t.Fatal("denying fence bypassed")
	}
	// real tar: regular file applies, dir + traversal path skip
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{Name: "note.md", Typeflag: tar.TypeReg, Size: 3})
	_, _ = tw.Write([]byte("abc"))
	_ = tw.WriteHeader(&tar.Header{Name: "adir/", Typeflag: tar.TypeDir})
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	var writes []string
	apply := func(_ context.Context, _ EffectiveBinding, req AgentExecRequest) (AgentExecResult, error) {
		writes = append(writes, fmt.Sprint(req.Arguments["path"]))
		return AgentExecResult{}, nil
	}
	allow := func(context.Context, EffectiveBinding) error { return nil }
	applied, skipped, err := ApplyExports(ctx, apply, allow, eff, AgentExecRequest{}, "ws", base64.StdEncoding.EncodeToString(tarBuf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !strings.HasSuffix(applied[0], "exports/note.md") || len(skipped) != 2 {
		t.Fatalf("applied=%v skipped=%v", applied, skipped)
	}
	if len(writes) != 1 {
		t.Fatalf("writes: %v", writes)
	}
	if _, _, err := ApplyExports(ctx, apply, allow, eff, AgentExecRequest{}, "ws", "!!!not-base64!!!"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad b64: %v", err)
	}
}
