package projectmemory

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSnapshotDirectorySyncOrderAndFailure(t *testing.T) {
	base := t.TempDir()
	leaf := filepath.Join(base, "namespace", "versions", "generation")
	want := []string{leaf, filepath.Dir(leaf), filepath.Dir(filepath.Dir(leaf)), base}
	var got []string
	if err := syncDirectoryChain(leaf, base, func(p string) error { got = append(got, p); return nil }); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("sync order %v: %v", got, err)
	}
	fault := errors.New("directory sync failed")
	got = nil
	err := syncDirectoryChain(leaf, base, func(p string) error {
		got = append(got, p)
		if len(got) == 2 {
			return fault
		}
		return nil
	})
	if !errors.Is(err, fault) || len(got) != 2 {
		t.Fatalf("failure swallowed: %v %v", got, err)
	}
}
