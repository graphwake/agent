package spec

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testReader() *Reader {
	return &Reader{
		HTTP:       &http.Client{Timeout: 200 * time.Millisecond},
		MaxBytes:   1024,
		Attempts:   5,
		Backoff:    10 * time.Millisecond,
		MaxBackoff: 40 * time.Millisecond,
		Deadline:   5 * time.Second,
		Sleep:      SleepContext,
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "openapi.yaml")
	os.WriteFile(path, []byte("openapi: 3.0.0\n"), 0o600)
	got, err := testReader().Read(context.Background(), path)
	if err != nil || got != "openapi: 3.0.0\n" {
		t.Fatalf("Read = %q, %v", got, err)
	}

	_, err = testReader().Read(context.Background(), filepath.Join(dir, "missing.json"))
	if err == nil || !strings.Contains(err.Error(), "spec file not found") {
		t.Fatalf("missing file err = %v", err)
	}

	big := filepath.Join(dir, "big.json")
	os.WriteFile(big, make([]byte, 1025), 0o600)
	if _, err := testReader().Read(context.Background(), big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big file err = %v, want ErrTooLarge", err)
	}
}

func TestFetchSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"openapi":"3.0.0"}`))
	}))
	defer srv.Close()
	got, err := testReader().Read(context.Background(), srv.URL+"/swagger/v1/swagger.json")
	if err != nil || got != `{"openapi":"3.0.0"}` {
		t.Fatalf("Read = %q, %v", got, err)
	}
}

func TestFetchRetries5xxThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte("openapi: 3.1.0"))
	}))
	defer srv.Close()
	got, err := testReader().Read(context.Background(), srv.URL)
	if err != nil || got != "openapi: 3.1.0" || calls.Load() != 3 {
		t.Fatalf("Read = %q, %v after %d calls", got, err, calls.Load())
	}
}

func TestFetchGivesUpAfterAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := testReader().Read(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "answered 500") || calls.Load() != 5 {
		t.Fatalf("err = %v after %d calls, want 5 attempts", err, calls.Load())
	}
}

func TestFetchDoesNotRetry4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := testReader().Read(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "answered 404") || calls.Load() != 1 {
		t.Fatalf("err = %v after %d calls", err, calls.Load())
	}
}

// The application under test is still starting: the first connections are refused.
func TestFetchWaitsForLateServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens yet: connection refused

	r := testReader()
	r.Backoff = 50 * time.Millisecond
	r.MaxBackoff = 100 * time.Millisecond
	started := make(chan *http.Server, 1)
	go func() {
		time.Sleep(120 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			started <- nil
			return
		}
		s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ready")) })}
		started <- s
		s.Serve(l)
	}()
	got, err := r.Read(context.Background(), "http://"+addr+"/openapi.json")
	if s := <-started; s != nil {
		defer s.Close()
	} else {
		t.Skip("could not re-bind the port")
	}
	if err != nil || got != "ready" {
		t.Fatalf("Read = %q, %v", got, err)
	}
}

func TestFetchTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	r := testReader()
	r.Deadline = 600 * time.Millisecond
	start := time.Now()
	_, err := r.Read(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "could not reach spec URL") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %v, the deadline was not enforced", elapsed)
	}
}

func TestFetchTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 2048))
	}))
	defer srv.Close()
	if _, err := testReader().Read(context.Background(), srv.URL); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestSafeURLDropsCredentialsAndQuery(t *testing.T) {
	got := SafeURL("https://user:secret@host:8080/openapi.json?token=abc#x")
	if got != "https://host:8080/openapi.json" {
		t.Fatalf("SafeURL = %q", got)
	}
}

func TestIsURL(t *testing.T) {
	for in, want := range map[string]bool{
		"http://localhost:5000/swagger/v1/swagger.json": true,
		"HTTPS://x/y":    true,
		"./openapi.yaml": false,
		"C:\\spec.json":  false,
	} {
		if IsURL(in) != want {
			t.Errorf("IsURL(%q) != %v", in, want)
		}
	}
}
