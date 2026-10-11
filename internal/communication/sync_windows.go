package communication

// Windows does not support directory fsync through os.File; file contents are
// synced before rename. Production durability is verified on Linux volumes.
func syncSpoolDirectory(string) error { return nil }
