package projectmemory

// Windows/Go does not expose a portable directory fsync guarantee. File.Sync
// protects process-crash persistence, but cannot promise directory survival on
// sudden power loss. Do not advertise host-crash durability on this platform.
func syncSnapshotDirectories(dir, ancestor string) error { return nil }
