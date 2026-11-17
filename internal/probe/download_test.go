package probe_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestDownloadBytesCounts(t *testing.T) {
	const total = 1 << 20
	chunk := bytes.Repeat([]byte("x"), 32<<10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for written := 0; written < total; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	n, err := probe.DownloadBytes(context.Background(), &redirectDialer{}, srv.URL)
	if err != nil {
		t.Fatalf("download %s: %v", srv.URL, err)
	}
	if n != total {
		t.Errorf("bytes = %d, want %d", n, total)
	}
}

// TestDownloadBytesStatusError pins that a non-2xx is an error naming the
// status and reports no load: an error body is not traffic the burst competed
// with, so the caller must discard the loaded sample.
func TestDownloadBytesStatusError(t *testing.T) {
	body := []byte("no such file")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	n, err := probe.DownloadBytes(context.Background(), &redirectDialer{}, srv.URL)
	if err == nil {
		t.Fatalf("404: err = nil, want an error naming the status")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("404")) {
		t.Errorf("error = %q, want it to name the 404 status", err)
	}
	if n != 0 {
		t.Errorf("bytes = %d, want 0: an error body is not load", n)
	}
}

// TestDownloadBytesRedirectYieldsNoLoad pins the redirect case: the client does
// not follow redirects, so a redirecting under-load URL arrives as a 302 with a
// body. Counting that body would publish a loaded sample measured with no
// competing traffic, which reads as "no congestion under load".
func TestDownloadBytesRedirectYieldsNoLoad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file.bin", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	n, err := probe.DownloadBytes(context.Background(), &redirectDialer{}, srv.URL)
	if err == nil {
		t.Fatalf("302: err = nil, want an error naming the status")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("302")) {
		t.Errorf("error = %q, want it to name the 302 status", err)
	}
	if n != 0 {
		t.Errorf("bytes = %d, want 0: a redirect body is not load", n)
	}
}

// TestDownloadBytesContextCancel pins that a cancel mid-body returns the bytes
// moved so far alongside the context error: a partial transfer is real load.
func TestDownloadBytesContextCancel(t *testing.T) {
	const (
		chunkSize = 64 << 10
		chunks    = 10
	)
	chunk := bytes.Repeat([]byte("x"), chunkSize)
	first := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < chunks; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if i == 0 {
				close(first)
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-first
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	n, err := probe.DownloadBytes(ctx, &redirectDialer{}, srv.URL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n <= 0 || n >= chunks*chunkSize {
		t.Errorf("bytes = %d, want a partial count in (0, %d)", n, chunks*chunkSize)
	}
}
