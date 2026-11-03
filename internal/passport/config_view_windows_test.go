//go:build windows

package passport

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func dirInode(t *testing.T, p string) uint64 {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		t.Fatal(err)
	}
	return uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
}
