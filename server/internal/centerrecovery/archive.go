// Package centerrecovery stores complete, operator-authorized center snapshots.
// Execution caches must never be presented as database backups.
package centerrecovery

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const MaxArchiveSize = 512 << 20
const magic = "MULTICA-RECOVERY-1\n"

type Manifest struct {
	Version       int       `json:"version"`
	CenterID      string    `json:"center_id"`
	CreatedAt     time.Time `json:"created_at"`
	ServerVersion string    `json:"server_version"`
	Storage       string    `json:"storage"`
}

// DeploymentKeys are the keys needed to read encrypted application records.
// Database credentials and live login/session secrets are not exported here.
var DeploymentKeys = []string{"MULTICA_PLUGIN_SECRET_KEY", "MULTICA_VCS_SECRET_KEY",
	"MULTICA_LARK_SECRET_KEY", "MULTICA_SLACK_SECRET_KEY", "MULTICA_DINGTALK_SECRET_KEY",
	"MULTICA_TELEGRAM_SECRET_KEY", "MULTICA_WECOM_SECRET_KEY"}

type CaptureOptions struct {
	CenterID, ServerVersion, DatabaseURL, UploadDir, PGDump string
	Secrets                                                 map[string]string
}

// DatabaseEnvironment avoids exposing database passwords in process arguments.
// Only URI-style PostgreSQL configuration is accepted, with explicit TLS options.
func DatabaseEnvironment(value string) ([]string, string, error) {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.Fragment != "" || u.Opaque != "" || strings.Trim(u.Path, "/") == "" {
		return nil, "", errors.New("recovery needs a PostgreSQL database URI")
	}
	name := strings.TrimPrefix(u.Path, "/")
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	password, _ := u.User.Password()
	values := map[string]string{"PGHOST": u.Hostname(), "PGPORT": port, "PGUSER": u.User.Username(), "PGPASSWORD": password, "PGDATABASE": name, "PGCONNECT_TIMEOUT": "10"}
	options := map[string]string{"sslmode": "PGSSLMODE", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "sslrootcert": "PGSSLROOTCERT", "options": "PGOPTIONS"}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, "", errors.New("invalid recovery database URI options")
	}
	for key, vv := range query {
		env, ok := options[key]
		if !ok || len(vv) != 1 {
			return nil, "", errors.New("unsupported recovery database URI option")
		}
		values[env] = vv[0]
	}
	if values["PGSSLMODE"] == "" {
		values["PGSSLMODE"] = "prefer"
	}
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PG") {
			env = append(env, entry)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env, name, nil
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxArchiveSize {
		return 0, errors.New("recovery archive exceeds 512 MiB limit")
	}
	return b.Buffer.Write(p)
}

func Capture(ctx context.Context, options CaptureOptions) ([]byte, error) {
	if options.UploadDir == "" {
		return nil, errors.New("local upload directory is required")
	}
	if options.CenterID == "" {
		return nil, errors.New("MULTICA_RECOVERY_CENTER_ID is required")
	}
	env, databaseName, err := DatabaseEnvironment(options.DatabaseURL)
	if err != nil {
		return nil, err
	}
	var out boundedBuffer
	zw := zip.NewWriter(&out)
	write := func(name string, data []byte) error {
		w, e := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if e != nil {
			return e
		}
		_, e = w.Write(data)
		return e
	}
	manifest, _ := json.Marshal(Manifest{1, options.CenterID, time.Now().UTC(), options.ServerVersion, "postgres+local"})
	if err := write("manifest.json", manifest); err != nil {
		return nil, err
	}
	secrets := make(map[string]string)
	for _, key := range DeploymentKeys {
		if v := options.Secrets[key]; v != "" {
			secrets[key] = v
		}
	}
	encoded, _ := json.Marshal(secrets)
	if err := write("deployment-keys.json", encoded); err != nil {
		return nil, err
	}
	dump, err := zw.CreateHeader(&zip.FileHeader{Name: "database.dump", Method: zip.Store})
	if err != nil {
		return nil, err
	}
	binary := options.PGDump
	if binary == "" {
		binary = "pg_dump"
	}
	cmd := exec.CommandContext(ctx, binary, "--format=custom", "--no-owner", "--no-acl", "--dbname", connectionDBName(databaseName))
	cmd.Env, cmd.Stdout, cmd.Stderr = env, dump, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("pg_dump failed; check PostgreSQL client installation and database access")
	}
	if options.UploadDir != "" {
		err = filepath.WalkDir(options.UploadDir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if entry.IsDir() {
				return nil
			}
			info, e := entry.Info()
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() {
				return errors.New("upload backup contains a non-regular file")
			}
			rel, e := filepath.Rel(options.UploadDir, path)
			if e != nil {
				return e
			}
			file, e := os.Open(path)
			if e != nil {
				return e
			}
			defer file.Close()
			w, e := zw.CreateHeader(&zip.FileHeader{Name: "uploads/" + filepath.ToSlash(rel), Method: zip.Store})
			if e != nil {
				return e
			}
			_, e = io.Copy(w, file)
			return e
		})
		if err != nil {
			return nil, errors.New("could not capture local uploads")
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if _, err := Inspect(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func Inspect(data []byte) (Manifest, error) {
	var manifest Manifest
	if len(data) > MaxArchiveSize {
		return manifest, errors.New("archive too large")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return manifest, errors.New("invalid recovery archive")
	}
	seen := map[string]bool{}
	var total uint64
	for _, f := range zr.File {
		if seen[f.Name] || !fs.ValidPath(f.Name) || strings.ContainsAny(f.Name, "\\:") || !f.Mode().IsRegular() {
			return manifest, errors.New("invalid archive entry")
		}
		seen[f.Name] = true
		if f.Name != "manifest.json" && f.Name != "database.dump" && f.Name != "deployment-keys.json" && !strings.HasPrefix(f.Name, "uploads/") {
			return manifest, errors.New("unknown archive entry")
		}
		if f.UncompressedSize64 > MaxArchiveSize-total {
			return manifest, errors.New("expanded recovery archive exceeds limit")
		}
		total += f.UncompressedSize64
		r, e := f.Open()
		if e != nil {
			return manifest, e
		}
		if f.Name == "manifest.json" {
			if f.UncompressedSize64 > 65536 {
				r.Close()
				return manifest, errors.New("manifest too large")
			}
			var raw []byte
			raw, e = io.ReadAll(r)
			if e == nil {
				e = json.Unmarshal(raw, &manifest)
			}
		} else if f.Name == "database.dump" {
			header := make([]byte, 5)
			_, e = io.ReadFull(r, header)
			if e == nil && string(header) != "PGDMP" {
				e = errors.New("invalid PostgreSQL dump")
			}
			if e == nil {
				_, e = io.Copy(io.Discard, r)
			}
		} else {
			_, e = io.Copy(io.Discard, r)
		}
		r.Close()
		if e != nil {
			return manifest, errors.New("archive integrity check failed")
		}
	}
	if !seen["database.dump"] || !seen["deployment-keys.json"] || manifest.Version != 1 || manifest.CenterID == "" || manifest.CreatedAt.IsZero() || manifest.Storage != "postgres+local" {
		return manifest, errors.New("incomplete recovery archive")
	}
	return manifest, nil
}

func key(root string) ([]byte, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "recovery.key")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = make([]byte, 32)
		if _, err = rand.Read(data); err != nil {
			return nil, err
		}
		f, e := os.CreateTemp(root, ".key-*")
		if e != nil {
			return nil, e
		}
		defer os.Remove(f.Name())
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		// Publish a fully written key without replacing a concurrent writer's key.
		err = os.Link(f.Name(), path)
		if os.IsExist(err) {
			return key(root)
		}

	}
	if err != nil {
		return nil, err
	}
	if len(data) != 32 {
		return nil, errors.New("invalid recovery encryption key")
	}
	return data, nil
}

func Save(root, origin string, data []byte) (string, error) {
	manifest, err := Inspect(data)
	if err != nil {
		return "", err
	}
	k, err := key(root)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(k)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := append([]byte(magic), nonce...)
	ciphertext = aead.Seal(ciphertext, nonce, data, []byte(magic))
	scope := sha256.Sum256([]byte(origin + "\n" + manifest.CenterID))
	dir := filepath.Join(root, hex.EncodeToString(scope[:16]))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(ciphertext)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	name := filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000000000Z")+".mcr")
	if err = os.Rename(f.Name(), name); err != nil {
		return "", err
	}
	return name, nil
}

func Read(root, path string) ([]byte, Manifest, error) {
	var manifest Manifest
	f, err := os.Open(path)
	if err != nil {
		return nil, manifest, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxArchiveSize+1024))
	if err != nil {
		return nil, manifest, err
	}
	if len(data) > MaxArchiveSize+128 || !bytes.HasPrefix(data, []byte(magic)) {
		return nil, manifest, errors.New("invalid encrypted recovery archive")
	}
	k, err := os.ReadFile(filepath.Join(root, "recovery.key"))
	if err != nil {
		return nil, manifest, errors.New("recovery key is unavailable")
	}
	if len(k) != 32 {
		return nil, manifest, errors.New("invalid recovery encryption key")
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, manifest, err
	}
	aead, _ := cipher.NewGCM(block)
	data = data[len(magic):]
	if len(data) < aead.NonceSize() {
		return nil, manifest, errors.New("truncated recovery archive")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(magic))
	if err != nil {
		return nil, manifest, errors.New("recovery archive authentication failed")
	}
	manifest, err = Inspect(plain)
	return plain, manifest, err
}

func Extract(data []byte, directory string) error {
	if _, err := Inspect(data); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return fmt.Errorf("restore needs a new directory: %w", err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	for _, entry := range zr.File {
		dest := filepath.Join(directory, filepath.FromSlash(entry.Name))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		r, err := entry.Open()
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(f, r)
		r.Close()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
