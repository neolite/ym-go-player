package httpapi

import (
	"context"
	"net/http"
	"testing"
)

func TestServerServesHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := New(mux)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	resp, err := http.Get("http://" + srv.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServerBindsLoopbackOnly(t *testing.T) {
	srv := New(http.NewServeMux())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	if got := srv.Addr(); len(got) < 10 || got[:10] != "127.0.0.1:" {
		t.Fatalf("Addr = %q, want 127.0.0.1:<port>", got)
	}
}
