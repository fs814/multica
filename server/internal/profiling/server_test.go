package profiling

import (
	"net"
	"net/http"
	"testing"
)

func TestCenterProfilingCoexists(t *testing.T) {
	t.Setenv("MULTICA_CENTER_ONLY", "0")
	if got := NewServer().Addr; got != Addr {
		t.Fatalf("default addr = %s", got)
	}
	t.Setenv("MULTICA_CENTER_ONLY", "1")
	var addresses []string
	for range 2 {
		server := NewServer()
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
		host, port, err := net.SplitHostPort(listener.Addr().String())
		if err != nil || host != "127.0.0.1" || port == "0" {
			t.Fatalf("unsafe or unresolved address: %s", listener.Addr())
		}
		addresses = append(addresses, listener.Addr().String())
		go func() { _ = server.Serve(listener) }()
		resp, err := http.Get("http://" + listener.Addr().String() + "/debug/pprof/")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatal(resp.Status)
		}
	}
	if addresses[0] == addresses[1] {
		t.Fatal("centers share a profiling port")
	}
}
