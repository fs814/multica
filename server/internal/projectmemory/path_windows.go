//go:build windows

package projectmemory

import (
	"io/fs"
	"syscall"
)

// Junctions need not carry ModeSymlink in Go; reject every reparse-point kind.
func unsafeReparse(info fs.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
