//go:build windows

package stack

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestAtomicRenderWaitsForWindowsReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool-manifest.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		closed <- windows.CloseHandle(handle)
	}()
	err = atomic(path, []byte("new"))
	if closeErr := <-closed; closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "new" {
		t.Fatalf("rendered content = %q, error = %v", body, err)
	}
	left, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".render-*"))
	if err != nil || len(left) != 0 {
		t.Fatalf("temporary renders remain: %v, error = %v", left, err)
	}
}
