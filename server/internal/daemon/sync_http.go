package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/worksync"
)

var ErrSyncUnavailable = errors.New("work sync transport unavailable")

// HTTPWorkSyncTransport pins one origin and obtains only the dedicated daemon
// credential. Redirects are rejected, including same-origin redirects; neither
// credentials nor operations may migrate to a newly advertised Center.
type HTTPWorkSyncTransport struct {
	base   string
	token  func() (string, error)
	client *http.Client
}

func NewHTTPWorkSyncTransport(base string, token func() (string, error)) (*HTTPWorkSyncTransport, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || token == nil {
		return nil, worksync.ErrScope
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return nil, worksync.ErrScope
	}
	return &HTTPWorkSyncTransport{base: strings.TrimSuffix(base, "/"), token: token, client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (t *HTTPWorkSyncTransport) call(ctx context.Context, action string, req worksync.Request, out any) error {
	token, err := t.token()
	if err != nil {
		return fmt.Errorf("read sync credential: %w", err)
	}
	if !strings.HasPrefix(token, "mdt_") {
		return worksync.ErrUnauthenticated
	}
	body, err := json.Marshal(req)
	if err != nil || len(body) > worksync.MaxWireBytes {
		return worksync.ErrOperation
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+worksync.HTTPPrefix+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	response, err := t.client.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Do not include an arbitrary server URL or response body in diagnostics.
		return ErrSyncUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case 401:
		return worksync.ErrUnauthenticated
	case 403:
		return worksync.ErrDenied
	case 404:
		return worksync.ErrDisabled
	case 400:
		return worksync.ErrOperation
	case 409:
		return worksync.ErrScope
	case 507:
		return worksync.ErrLimit
	default:
		if response.StatusCode == 429 || response.StatusCode >= 500 {
			return ErrSyncUnavailable
		}
		return worksync.ErrScope
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, worksync.MaxWireBytes+1))
	if err != nil {
		return ErrSyncUnavailable
	}
	if len(data) > worksync.MaxWireBytes {
		return worksync.ErrOperation
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return worksync.ErrOperation
	}
	return nil
}
func (t *HTTPWorkSyncTransport) Handshake(ctx context.Context, p worksync.Principal, s worksync.Scope) error {
	var out worksync.Handshake
	if err := t.call(ctx, "handshake", worksync.Request{Schema: worksync.Schema, Scope: s}, &out); err != nil {
		return err
	}
	if out.Schema != worksync.Schema || out.Scope != s || out.Principal != p || out.MaxBatch != worksync.MaxBatch {
		return worksync.ErrScope
	}
	return nil
}
func (t *HTTPWorkSyncTransport) Pull(ctx context.Context, p worksync.Principal, s worksync.Scope, cursor int64, snapshot bool) (worksync.Batch, error) {
	var out worksync.Batch
	err := t.call(ctx, "pull", worksync.Request{Schema: worksync.Schema, Scope: s, Cursor: cursor, Snapshot: snapshot}, &out)
	if err == nil {
		err = out.Validate(s)
	}
	return out, err
}
func (t *HTTPWorkSyncTransport) Push(ctx context.Context, p worksync.Principal, s worksync.Scope, op worksync.Operation) (worksync.Receipt, error) {
	var out worksync.Receipt
	if err := op.Validate(p, s); err != nil {
		return out, err
	}
	err := t.call(ctx, "push", worksync.Request{Schema: worksync.Schema, Scope: s, Operation: &op}, &out)
	return out, err
}
