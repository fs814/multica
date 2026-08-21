//go:build windows

package wecom

import "os"

// Windows denies deleting a file opened without FILE_SHARE_DELETE. Keep the
// 0600/user-ACL-protected file named until closeAndRemoveTempFile closes it.
func unlinkOpenTempFile(_ *os.File) error {
	return nil
}
