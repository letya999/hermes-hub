//go:build windows

package api

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestGovernanceReplacementWaitsForWindowsReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "governance.json")
	doc := NewGovernance()
	if err := SaveGovernanceLocked(path, doc); err != nil {
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
	go func() { time.Sleep(50 * time.Millisecond); closed <- windows.CloseHandle(handle) }()
	doc.Revision++
	err = SaveGovernanceLocked(path, doc)
	if closeErr := <-closed; closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	actual, err := LoadGovernance(path)
	if err != nil || actual.Revision != doc.Revision {
		t.Fatal("governance replacement did not publish the complete next revision")
	}
	left, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".governance-*"))
	if err != nil || len(left) != 0 {
		t.Fatal("governance replacement leaked temporary files")
	}
}

func TestGovernanceReplacementPreservesPreviousOnBlockedReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "governance.json")
	doc := NewGovernance()
	if err := SaveGovernanceLocked(path, doc); err != nil {
		t.Fatal(err)
	}
	previous := doc.Revision
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	doc.Revision++
	started := time.Now()
	err = SaveGovernanceLocked(path, doc)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatal("blocked policy replacement did not report its sharing failure")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("blocked policy replacement exceeded its bounded retry")
	}
	actual, err := LoadGovernance(path)
	if err != nil || actual.Revision != previous {
		t.Fatal("failed replacement changed the previous policy")
	}
	left, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".governance-*"))
	if err != nil || len(left) != 0 {
		t.Fatal("failed replacement leaked temporary policy files")
	}
}
