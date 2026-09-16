package projectmemory

import "path/filepath"

// Sync bottom-up including the pre-existing parent that gained a new child.
// Keeping ordering here permits deterministic fault/order tests on every OS.
func syncDirectoryChain(dir, ancestor string, syncDir func(string) error) error {
	for {
		if err := syncDir(dir); err != nil {
			return err
		}
		if dir == ancestor {
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}
