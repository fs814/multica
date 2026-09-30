package centerrecovery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandlerAuthorizationAndFailedCapture(t *testing.T) {
	token := strings.Repeat("a", 64)
	calls := 0
	h := Handler(token, func(context.Context) ([]byte, error) { calls++; return nil, errors.New("sensitive database detail") })
	for _, credential := range []string{"", "Bearer wrong", "Bearer mul_ordinary-user", "Basic " + token} {
		req := httptest.NewRequest(http.MethodGet, Endpoint, nil)
		req.Header.Set("Authorization", credential)
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != 401 || calls != 0 {
			t.Fatal("unauthorized export", w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, Endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != 503 || strings.Contains(w.Body.String(), "sensitive") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("failed export leaked details or succeeded")
	}
	w = httptest.NewRecorder()
	Handler("", nil)(w, req)
	if w.Code != 404 {
		t.Fatal("endpoint enabled without credential")
	}
}
func TestPullPreservesSnapshotsAndNeverFollowsRedirects(t *testing.T) {
	root := t.TempDir()
	data := fixture(t, nil)
	token := strings.Repeat("a", 64)
	targetCalls := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++; w.WriteHeader(200) }))
	defer other.Close()
	mode := "ok"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong credential")
		}
		switch mode {
		case "redirect":
			http.Redirect(w, r, other.URL, 307)
		case "partial":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(data[:len(data)/2])
		default:
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(data)
		}
	}))
	defer source.Close()
	s := Source{Origin: source.URL, Token: token}
	if e := Configure(root, s); e != nil {
		t.Fatal(e)
	}
	loaded, e := LoadSource(root, source.URL)
	if e != nil || loaded.Token != token {
		t.Fatal("configuration not saved", e)
	}
	if _, e = LoadSource(root, other.URL); !os.IsNotExist(e) {
		t.Fatal("borrowed another origin's credential", e)
	}
	path, e := Pull(context.Background(), root, s)
	if e != nil {
		t.Fatal(e)
	}
	for _, failure := range []string{"redirect", "partial"} {
		mode = failure
		if _, e = Pull(context.Background(), root, s); e == nil {
			t.Fatal("accepted failed download", failure)
		}
	}
	if targetCalls != 0 {
		t.Fatal("followed cross-origin redirect")
	}
	if _, _, e = Read(root, path); e != nil {
		t.Fatal("lost previous snapshot", e)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.mcr"))
	if len(files) != 1 {
		t.Fatal("saved invalid responses")
	}
}
func TestSourceValidation(t *testing.T) {
	for _, raw := range []string{"file:///tmp/x", "https://user:pass@host", "https://host/path", "https://host/?token=x", "https://host/#fragment"} {
		if _, e := Origin(raw); e == nil {
			t.Fatal("accepted unsafe origin", raw)
		}
	}
	origin, e := Origin("https://CENTER.example/")
	if e != nil || origin != "https://center.example" {
		t.Fatal(origin, e)
	}
	s := Source{Origin: "http://192.168.1.1:18080", Token: strings.Repeat("a", 64)}
	if e := s.validate(); e == nil {
		t.Fatal("accepted remote cleartext without opt-in")
	}
	s.AllowHTTP = true
	if e := s.validate(); e != nil {
		t.Fatal(e)
	}
}
