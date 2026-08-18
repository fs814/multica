//go:build !windows

package wecom

import "os"

func unlinkOpenTempFile(f *os.File) error {
	return os.Remove(f.Name())
}
