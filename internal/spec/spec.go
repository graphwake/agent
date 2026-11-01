// Package spec reads the OpenAPI document to send, from a file or from an http(s) URL. The content is passed on as
// is (YAML or JSON); Graphwake validates it.
package spec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrTooLarge means the spec alone is already over the size limit.
var ErrTooLarge = errors.New("spec too large")

// Reader reads a spec. The zero value is not usable; use NewReader.
type Reader struct {
	HTTP *http.Client
	// MaxBytes: a spec larger than this is refused with ErrTooLarge.
	MaxBytes int64
	// Attempts, Backoff, MaxBackoff and Deadline bound the URL retries (refused connections and 5xx answers).
	Attempts   int
	Backoff    time.Duration
	MaxBackoff time.Duration
	Deadline   time.Duration
	Sleep      func(context.Context, time.Duration)
}

// NewReader returns the production settings: 30 s per request, 5 attempts, at most ~60 s overall.
func NewReader(maxBytes int64) *Reader {
	return &Reader{
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		MaxBytes:   maxBytes,
		Attempts:   5,
		Backoff:    2 * time.Second,
		MaxBackoff: 8 * time.Second,
		Deadline:   60 * time.Second,
		Sleep:      SleepContext,
	}
}

// SleepContext waits d or until ctx ends.
func SleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// IsURL reports whether source is an http(s) URL rather than a file path.
func IsURL(source string) bool {
	l := strings.ToLower(source)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// Read returns the spec content of a file path or an http(s) URL.
func (r *Reader) Read(ctx context.Context, source string) (string, error) {
	if IsURL(source) {
		return r.fetch(ctx, source)
	}
	return r.readFile(source)
}

func (r *Reader) readFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("spec file not found: %s", path)
		}
		return "", fmt.Errorf("cannot read spec file %s", path)
	}
	defer f.Close()
	return r.readLimited(f, "spec file "+path)
}

func (r *Reader) readLimited(src io.Reader, what string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(src, r.MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("cannot read %s", what)
	}
	if int64(len(data)) > r.MaxBytes {
		return "", ErrTooLarge
	}
	return string(data), nil
}

type retryable struct{ error }

func (r *Reader) fetch(ctx context.Context, raw string) (string, error) {
	shown := SafeURL(raw)
	ctx, cancel := context.WithTimeout(ctx, r.Deadline)
	defer cancel()

	wait := r.Backoff
	var last error
	for attempt := 1; attempt <= r.Attempts; attempt++ {
		body, err := r.fetchOnce(ctx, raw, shown)
		if err == nil {
			return body, nil
		}
		var retry retryable
		if !errors.As(err, &retry) {
			return "", err
		}
		last = retry.error
		if attempt == r.Attempts || ctx.Err() != nil {
			break
		}
		r.Sleep(ctx, wait)
		if ctx.Err() != nil {
			break
		}
		wait *= 2
		if wait > r.MaxBackoff {
			wait = r.MaxBackoff
		}
	}
	return "", last
}

func (r *Reader) fetchOnce(ctx context.Context, raw, shown string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", fmt.Errorf("invalid spec URL %s", shown)
	}
	req.Header.Set("Accept", "application/json, application/yaml, text/yaml, */*")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", retryable{fmt.Errorf("could not reach spec URL %s", shown)}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 500:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return "", retryable{fmt.Errorf("spec URL %s answered %d", shown, resp.StatusCode)}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return "", fmt.Errorf("spec URL %s answered %d", shown, resp.StatusCode)
	}
	body, err := r.readLimited(resp.Body, "spec URL "+shown)
	if err != nil && !errors.Is(err, ErrTooLarge) {
		return "", retryable{err}
	}
	return body, err
}

// SafeURL renders a URL for logs without credentials, query or fragment (they may carry tokens).
func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
