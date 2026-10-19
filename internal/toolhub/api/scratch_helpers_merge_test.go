package api

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestScratchConfirmStopped(t *testing.T) {
	ctx := context.Background()
	if ShortErr([]byte("  long  ")) != "long" || len(ShortErr([]byte(strings.Repeat("x", 300)))) != 200 {
		t.Fatal("ShortErr")
	}
	// rm failure means the executor may still run
	rmFail := func(context.Context, io.Reader, ...string) ([]byte, []byte, error) {
		return nil, []byte("denied"), errors.New("rm failed")
	}
	if ok, err := confirmStopped(ctx, rmFail, "c1"); ok || err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("rm failure: %v %v", ok, err)
	}
	// a surviving inspect answer means not stopped
	alive := func(_ context.Context, _ io.Reader, args ...string) ([]byte, []byte, error) {
		if args[0] == "inspect" {
			return []byte("{}"), nil, nil
		}
		return nil, nil, nil
	}
	if ok, err := confirmStopped(ctx, alive, "c1"); ok || err != nil {
		t.Fatalf("survivor: %v %v", ok, err)
	}
	gone := func(_ context.Context, _ io.Reader, args ...string) ([]byte, []byte, error) {
		if args[0] == "inspect" {
			return nil, nil, errors.New("no such")
		}
		return nil, nil, nil
	}
	if ok, err := confirmStopped(ctx, gone, "c1"); !ok || err != nil {
		t.Fatalf("gone: %v %v", ok, err)
	}
}
