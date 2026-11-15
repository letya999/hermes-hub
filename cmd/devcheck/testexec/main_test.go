package main

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestRetriesOnlyLaunchPermissionFailure(t *testing.T) {
	attempts, pauses := 0, 0
	code := run(func() error {
		attempts++
		if attempts < 3 {
			return os.ErrPermission
		}
		return nil
	}, func(time.Duration) { pauses++ })
	if code != 0 || attempts != 3 || pauses != 2 {
		t.Fatalf("retry: code=%d attempts=%d pauses=%d", code, attempts, pauses)
	}
	attempts = 0
	code = run(func() error { attempts++; return errors.New("test failed") }, func(time.Duration) { t.Fatal("unexpected retry") })
	if code != 1 || attempts != 1 {
		t.Fatalf("non-permission error retried: code=%d attempts=%d", code, attempts)
	}
}
