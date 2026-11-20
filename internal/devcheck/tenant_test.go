package devcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTenantFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A package whose production code touches the scoped store or ships .sql
// migrations must carry TestTenantInvariants; everything else is ignored.
func TestTenantCoverage(t *testing.T) {
	root := t.TempDir()
	writeTenantFixture(t, filepath.Join(root, "internal", "clean"), "clean.go", "package clean\n")
	writeTenantFixture(t, filepath.Join(root, "internal", "owner"), "owner.go", "package owner\nimport \"github.com/letya999/hermes-hub/shared/store\"\nfunc f() { _ = tx.Scoped(tenant) }\n")
	writeTenantFixture(t, filepath.Join(root, "internal", "owner"), "owner_test.go", "package owner\nfunc helper() {}\n")
	writeTenantFixture(t, filepath.Join(root, "internal", "covered"), "c.go", "package covered\nimport \"github.com/letya999/hermes-hub/shared/store\"\nfunc f() { s.Tx(x).Scoped(t) }\n")
	writeTenantFixture(t, filepath.Join(root, "internal", "covered"), "c_test.go", "package covered\nfunc TestTenantInvariants(t *testing.T) {}\n")
	err := TenantCoverage(root)
	if err == nil || !strings.Contains(err.Error(), "internal/owner") {
		t.Fatalf("uncovered owner package accepted: %v", err)
	}
	if strings.Contains(err.Error(), "internal/clean") || strings.Contains(err.Error(), "internal/covered") {
		t.Fatalf("clean or covered package rejected: %v", err)
	}
	writeTenantFixture(t, filepath.Join(root, "internal", "owner"), "owner_test.go", "package owner\nfunc TestTenantInvariants(t *testing.T) {}\n")
	writeTenantFixture(t, filepath.Join(root, "services", "svc"), "svc.go", "package svc\n")
	writeTenantFixture(t, filepath.Join(root, "services", "svc", "migrations"), "0001_a.sql", "CREATE TABLE a (org TEXT, owner TEXT);")
	writeTenantFixture(t, filepath.Join(root, "services", "svc", "migrations"), "m_test.go", "package migrations\nfunc TestTenantInvariants(t *testing.T) {}\n")
	if err := TenantCoverage(root); err != nil {
		t.Fatalf("covered fixtures rejected: %v", err)
	}
}
