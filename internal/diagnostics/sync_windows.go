package diagnostics

// os.File cannot fsync Windows directories; Linux filesystem proof is required.
func syncDiagnosticDirectory(string) error { return nil }
