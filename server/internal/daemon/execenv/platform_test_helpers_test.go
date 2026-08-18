package execenv

import (
	"os"
	"runtime"
	"testing"
)

func setExecenvTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
}

func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	got := info.Mode().Perm()
	if runtime.GOOS == "windows" {
		// Windows DACLs are not represented by os.FileMode: regular files
		// report 0666 and directories 0777. Keep the assertion active by
		// verifying the current account retains every requested owner bit;
		// POSIX hosts below assert the exact least-privilege mode.
		if ownerWant := want.Perm() & 0o700; got&ownerWant != ownerWant {
			t.Fatalf("%s owner permissions = %#o, want at least %#o", path, got, ownerWant)
		}
		return
	}
	if got != want.Perm() {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want.Perm())
	}
}
