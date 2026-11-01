// Package ingest sends a spec and its CI context to Graphwake's POST /api/ingest/specs and turns every answer into
// one human-readable line. It never returns an error to fail the pipeline with.
package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxBodyBytes is the backend's limit on the whole encoded request body (10 MB).
const MaxBodyBytes = 10 * 1024 * 1024

// Context is the "context" object of the ingest request.
type Context struct {
	Kind       string `json:"kind"`
	PRID       string `json:"prId,omitempty"`
	PRURL      string `json:"prUrl,omitempty"`
	Author     string `json:"author,omitempty"`
	State      string `json:"state,omitempty"`
	Platform   string `json:"platform,omitempty"`
	CommitSHA  string `json:"commitSha,omitempty"`
	CommitTime string `json:"commitTime,omitempty"`
	// Branch is the branch the commit is on (at most 255 characters; longer names are not sent).
	Branch string `json:"branch,omitempty"`
}

// Body is the ingest request. Spec is nil when a pull request is closed (no spec needed).
type Body struct {
	Spec    *string `json:"spec,omitempty"`
	Context Context `json:"context"`
}

// ErrBodyTooLarge: the encoded body is over MaxBodyBytes.
var ErrBodyTooLarge = errors.New("spec too large (limit 10 MB), not sent")

// Encode marshals the body exactly as it is sent and enforces the 10 MB limit on the result.
func Encode(b Body) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	data := bytes.TrimRight(buf.Bytes(), "\n")
	if len(data) > MaxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	return data, nil
}

// Result is the outcome of a send: OK, or the warning to print.
type Result struct {
	OK      bool
	Status  int // 0 when no answer was received
	Warning string
}

// Client posts to the ingest endpoint with a few retries for network errors and 503.
type Client struct {
	BaseURL   string
	Key       string
	UserAgent string
	HTTP      *http.Client
	Attempts  int
	Backoff   time.Duration
	Sleep     func(context.Context, time.Duration)
	// Deadline caps the whole send including retries; AttemptTimeout caps one request. Zero means no cap.
	Deadline       time.Duration
	AttemptTimeout time.Duration
}

// Send posts an encoded body (from Encode).
func (c *Client) Send(ctx context.Context, body []byte) Result {
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/api/ingest/specs"
	if c.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Deadline)
		defer cancel()
	}
	wait := c.Backoff
	attempts := c.Attempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 1; ; attempt++ {
		status, respBody, err := c.post(ctx, endpoint, body)
		transient := err != nil || status == http.StatusServiceUnavailable
		if !transient || attempt >= attempts || ctx.Err() != nil {
			if err != nil {
				return Result{Warning: "Graphwake unavailable, spec not sent"}
			}
			return Interpret(status, respBody)
		}
		c.Sleep(ctx, wait)
		wait *= 2
	}
}

func (c *Client) post(ctx context.Context, endpoint string, body []byte) (int, []byte, error) {
	if c.AttemptTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.AttemptTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, data, nil
}

type errorBody struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// Interpret maps an answer of the ingest endpoint to a Result (the design's response table).
func Interpret(status int, body []byte) Result {
	var eb errorBody
	_ = json.Unmarshal(body, &eb) // empty or non-JSON bodies fall back to fixed texts
	r := Result{Status: status}
	switch {
	case status >= 200 && status <= 299:
		r.OK = true
	case status == http.StatusUnauthorized:
		r.Warning = "invalid or missing ingest key, spec not sent"
	case status == http.StatusPaymentRequired:
		r.Warning = "the Graphwake account is closed, spec not sent"
	case status == http.StatusConflict && eb.Code == "url_source":
		r.Warning = "this service takes its spec from a URL, not from CI; spec not sent"
	case status == http.StatusRequestEntityTooLarge:
		r.Warning = "spec too large (limit 10 MB), not sent"
	case status == http.StatusUnprocessableEntity:
		r.Warning = "spec rejected: " + orDefault(eb.Error, "invalid spec")
	case status == http.StatusBadRequest || status == http.StatusUnsupportedMediaType:
		r.Warning = "request rejected: " + orDefault(eb.Error, "bad request")
	case status == http.StatusServiceUnavailable:
		r.Warning = "Graphwake unavailable, spec not sent"
	default:
		r.Warning = fmt.Sprintf("unexpected response %d, spec not sent", status)
	}
	return r
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}
