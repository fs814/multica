package profiling

import (
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"time"
)

const Addr = "127.0.0.1:6060"

func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", httppprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", httppprof.Trace)
	return mux
}

func NewServer() *http.Server {
	addr := Addr
	if os.Getenv("MULTICA_CENTER_ONLY") == "1" {
		// A center can coexist with a desktop API using the default port.
		// Let the OS reserve a free loopback port atomically for this instance.
		addr = "127.0.0.1:0"
	}
	return &http.Server{
		Addr:              addr,
		Handler:           NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		// CPU profiles and traces run for a caller-selected duration. Leave
		// ReadTimeout and WriteTimeout unset so long captures are not truncated.
	}
}
