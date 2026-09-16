//go:build !windows

package projectmemory

import "os"

func syncSnapshotDirectories(dir, ancestor string) error {
	return syncDirectoryChain(dir, ancestor, func(path string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}
