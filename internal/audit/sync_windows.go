//go:build windows

package audit

// Windows directory sync is not a Linux durability proof.
func syncAuditDirectory(string) error { return nil }
