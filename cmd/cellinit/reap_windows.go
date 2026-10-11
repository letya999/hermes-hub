//go:build windows

package main

// Cells only run Linux images; the Windows build exists so host-side tooling
// and `go build ./...` stay green.
func reapOrphans() {}
func killProc(int) {}
