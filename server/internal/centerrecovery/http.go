package centerrecovery

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const Endpoint = "/api/center/recovery/snapshot"

// Handler is deliberately separate from workspace authentication: a recovery
// credential authorizes export of the entire deployment, including credentials.
func Handler(token string, capture func(context.Context) ([]byte, error)) http.HandlerFunc {
	var busy sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(token) < 32 {
			http.NotFound(w, r)
			return
		}
		if !authorizedRecovery(r, token) {
			http.Error(w, "recovery credential required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !busy.TryLock() {
			http.Error(w, "snapshot already in progress", http.StatusConflict)
			return
		}
		defer busy.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		data, err := capture(ctx)
		if err != nil {
			http.Error(w, "snapshot failed; check recovery configuration, storage and PostgreSQL tools", http.StatusServiceUnavailable)
			return
		}
		manifest, err := Inspect(data)
		if err != nil {
			http.Error(w, "invalid capture", 503)
			return
		}
		w.Header().Set("X-Multica-Recovery-Center-ID", manifest.CenterID)
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	}
}

func HandlerFromEnvironment(serverVersion string) http.HandlerFunc {
	options := CaptureOptions{CenterID: os.Getenv("MULTICA_RECOVERY_CENTER_ID"), ServerVersion: serverVersion,
		DatabaseURL: os.Getenv("DATABASE_URL"), UploadDir: os.Getenv("LOCAL_UPLOAD_DIR"), PGDump: os.Getenv("MULTICA_RECOVERY_PG_DUMP"), Secrets: map[string]string{}}
	if options.UploadDir == "" {
		options.UploadDir = "./data/uploads"
	}
	for _, key := range DeploymentKeys {
		options.Secrets[key] = os.Getenv(key)
	}
	s3 := os.Getenv("S3_BUCKET") != ""
	return Handler(os.Getenv("MULTICA_RECOVERY_TOKEN"), func(ctx context.Context) ([]byte, error) {
		if s3 {
			return nil, errors.New("recovery requires local upload storage")
		}
		return Capture(ctx, options)
	})
}

// Source never borrows a login token from another center or from the daemon.
type Source struct {
	Origin    string `json:"origin"`
	Token     string `json:"token"`
	AllowHTTP bool   `json:"allow_http,omitempty"`
}

func Origin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("source must be an HTTP(S) origin without credentials, path or query")
	}
	u.Path = ""
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}
func sourcePath(root, origin string) string {
	sum := sha256.Sum256([]byte(origin))
	return filepath.Join(root, "sources", hex.EncodeToString(sum[:])+".json")
}
func (s Source) validate() error {
	origin, err := Origin(s.Origin)
	if err != nil || origin != s.Origin {
		return errors.New("invalid recovery source origin")
	}
	if len(s.Token) < 32 || strings.ContainsAny(s.Token, " \t\r\n") {
		return errors.New("recovery token must contain at least 32 characters and no whitespace")
	}
	u, _ := url.Parse(origin)
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) && !s.AllowHTTP {
		return errors.New("remote recovery requires HTTPS; use --allow-http only on a trusted private network")
	}
	return nil
}
func Configure(root string, s Source) error {
	if err := s.validate(); err != nil {
		return err
	}
	target := sourcePath(root, s.Origin)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	data, _ := json.Marshal(s)
	f, err := os.CreateTemp(filepath.Dir(target), ".source-*")
	if err != nil {
		return err
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
		return err
	}
	return os.Rename(f.Name(), target)
}
func LoadSource(root, raw string) (Source, error) {
	var s Source
	origin, err := Origin(raw)
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(sourcePath(root, origin))
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(data, &s); err != nil {
		return Source{}, errors.New("invalid recovery source configuration")
	}
	if s.Origin != origin {
		return Source{}, errors.New("recovery source origin mismatch")
	}
	return s, s.validate()
}
func Pull(ctx context.Context, root string, s Source) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Origin+Endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	client := &http.Client{Timeout: 6 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "", errors.New("could not contact recovery source")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("source rejected recovery request; check source configuration and credential")
	}
	if response.Header.Get("Content-Type") != "application/zip" {
		return "", errors.New("source returned an unsupported recovery response")
	}
	if response.ContentLength > MaxArchiveSize {
		return "", errors.New("recovery response exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxArchiveSize+1))
	if err != nil {
		return "", errors.New("incomplete recovery download")
	}
	return Save(root, s.Origin, data)
}

func authorizedRecovery(r *http.Request, token string) bool {
	if len(token) < 32 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	expected := sha256.Sum256([]byte(token))
	actual := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
	return subtle.ConstantTimeCompare(expected[:], actual[:]) == 1
}
