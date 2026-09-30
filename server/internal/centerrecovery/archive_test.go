package centerrecovery

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T, mutate func(map[string][]byte)) []byte {
	t.Helper()
	manifest, _ := json.Marshal(Manifest{Version: 1, CenterID: "original-center", CreatedAt: time.Now().UTC(), Storage: "postgres+local"})
	files := map[string][]byte{"manifest.json": manifest, "database.dump": []byte("PGDMP-test-fixture"), "deployment-keys.json": []byte(`{"MULTICA_VCS_SECRET_KEY":"fixture-secret"}`), "uploads/issues/file.txt": []byte("attachment content")}
	if mutate != nil {
		mutate(files)
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for name, data := range files {
		w, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestSnapshotRoundTripAndCorruption(t *testing.T) {
	root := t.TempDir()
	data := fixture(t, nil)
	first, err := Save(root, "https://old.example", data)
	if err != nil {
		t.Fatal(err)
	}
	disk, _ := os.ReadFile(first)
	if bytes.Contains(disk, []byte("fixture-secret")) {
		t.Fatal("plaintext secret stored")
	}
	plain, m, err := Read(root, first)
	if err != nil || !bytes.Equal(data, plain) || m.CenterID != "original-center" {
		t.Fatalf("roundtrip: %v", err)
	}
	second, err := Save(root, "https://new.example", data)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(first) == filepath.Dir(second) {
		t.Fatal("different centers shared a snapshot namespace")
	}
	if _, err = Save(root, "https://old.example", []byte("partial response")); err == nil {
		t.Fatal("accepted failed download")
	}
	if _, _, err = Read(root, first); err != nil {
		t.Fatal("failed download damaged previous snapshot", err)
	}
	disk[len(disk)-1] ^= 1
	if err = os.WriteFile(first, disk, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Read(root, first); err == nil {
		t.Fatal("tampering accepted")
	}
}
func TestConcurrentFirstSnapshotsShareOneKey(t *testing.T) {
	root := t.TempDir()
	data := fixture(t, nil)
	var wg sync.WaitGroup
	paths := make(chan string, 12)
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() { p, e := Save(root, "https://center.example", data); paths <- p; errs <- e })
	}
	wg.Wait()
	close(paths)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for p := range paths {
		if _, _, e := Read(root, p); e != nil {
			t.Fatal("key replaced or partially read", e)
		}
	}
}
func TestArchiveValidationAndExtraction(t *testing.T) {
	cases := map[string]func(map[string][]byte){
		"traversal":         func(f map[string][]byte) { f["uploads/../../escape"] = []byte("bad") },
		"windows traversal": func(f map[string][]byte) { f[`uploads\..\escape`] = []byte("bad") },
		"missing database":  func(f map[string][]byte) { delete(f, "database.dump") },
		"invalid database":  func(f map[string][]byte) { f["database.dump"] = []byte("html error") },
		"missing keys":      func(f map[string][]byte) { delete(f, "deployment-keys.json") },
		"bad manifest":      func(f map[string][]byte) { f["manifest.json"] = []byte(`{"version":2}`) },
		"unknown file":      func(f map[string][]byte) { f["other"] = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, e := Inspect(fixture(t, mutate)); e == nil {
				t.Fatal("accepted malformed archive")
			}
		})
	}
	data := fixture(t, nil)
	dir := filepath.Join(t.TempDir(), "restore")
	if e := Extract(data, dir); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(filepath.Join(dir, "uploads", "issues", "file.txt"))
	if e != nil || string(got) != "attachment content" {
		t.Fatal("lost attachment", e)
	}
	if e := Extract(data, dir); e == nil {
		t.Fatal("overwrote existing restore directory")
	}
	if e := Restore(context.Background(), data, filepath.Join(t.TempDir(), "out"), "invalid", "wrong-center", ""); e == nil || !strings.Contains(e.Error(), "confirm-center") {
		t.Fatal("did not reject wrong center before database access")
	}
}
func TestDatabaseEnvironmentKeepsPasswordOutOfArguments(t *testing.T) {
	t.Setenv("PGSERVICE", "unrelated-service")
	t.Setenv("PGHOST", "unrelated-host")
	env, name, e := DatabaseEnvironment("postgres://owner:p%40ss@localhost:5433/db%3Dx?sslmode=require")
	if e != nil || name != "db=x" {
		t.Fatal(name, e)
	}
	values := map[string]string{}
	for _, v := range env {
		key, val, _ := strings.Cut(v, "=")
		values[key] = val
	}
	if values["PGHOST"] != "localhost" || values["PGPASSWORD"] != "p@ss" || values["PGSERVICE"] != "" || values["PGSSLMODE"] != "require" {
		t.Fatal("unexpected database environment")
	}
	if connectionDBName(`a'\b`) != `dbname='a\'\\b'` {
		t.Fatal("invalid connection name escaping")
	}
	for _, raw := range []string{"", "postgres://localhost/db", "postgres://u@host/db?host=attacker"} {
		if _, _, e := DatabaseEnvironment(raw); e == nil {
			t.Fatal("accepted invalid URI")
		}
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("MULTICA_TEST_RECOVERY_DUMP") == "1" {
		// Tests execute their own binary as pg_dump; never a user's database tool.
		if os.Getenv("PGPASSWORD") != "test-password" {
			os.Exit(2)
		}
		for _, arg := range os.Args[1:] {
			if strings.Contains(arg, "password") {
				os.Exit(3)
			}
		}
		fmt.Print("PGDMP-test-database")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestCaptureIncludesUploadsAndOnlyDeploymentKeys(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_TEST_RECOVERY_DUMP", "1")
	uploads := t.TempDir()
	if err = os.WriteFile(filepath.Join(uploads, "file.txt"), []byte("file payload"), 0600); err != nil {
		t.Fatal(err)
	}
	options := CaptureOptions{CenterID: "original-center", DatabaseURL: "postgres://owner:test-password@localhost/db", UploadDir: uploads, PGDump: binary, Secrets: map[string]string{"MULTICA_VCS_SECRET_KEY": "key-value", "JWT_SECRET": "not-exported"}}
	data, err := Capture(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("key-value")) || !bytes.Contains(data, []byte("file payload")) || bytes.Contains(data, []byte("not-exported")) || bytes.Contains(data, []byte("test-password")) {
		t.Fatal("incorrect archive contents")
	}
	options.UploadDir = filepath.Join(t.TempDir(), "missing-uploads")
	if _, err = Capture(context.Background(), options); err == nil {
		t.Fatal("reported missing uploads as a complete backup")
	}
	options.UploadDir = uploads
	options.PGDump = filepath.Join(t.TempDir(), "missing-pg-dump")
	if _, err = Capture(context.Background(), options); err == nil {
		t.Fatal("ignored pg_dump failure")
	}
}
