//go:build !windows

package projectmemory

import "io/fs"

func unsafeReparse(info fs.FileInfo) bool { return false }
