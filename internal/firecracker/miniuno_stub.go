//go:build !linux

// Package firecracker provides a non-Linux stub: the mini-Uno control plane
// only runs where KVM/Firecracker exists.
package firecracker

import (
	"errors"
	"net/http"
)

// Config is a placeholder on non-Linux builds.
type Config struct{}

// Manager reports unsupported on non-Linux builds.
type Manager struct{}

// NewManager always fails off Linux.
func NewManager(Config) (*Manager, error) {
	return nil, errors.New("mini-uno requires Linux with /dev/kvm")
}

// ServeHTTP is a stub.
func (m *Manager) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "mini-uno requires Linux", http.StatusServiceUnavailable)
}
