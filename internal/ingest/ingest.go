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

// PullRequestHints is the optional "pullRequest" object of the ingest request: what the server needs to write the
// pull request comment and the gate decision. It is sent only on a pull request build.
type PullRequestHints struct {
	// CommentFormat is "github" or "azure-devops", sent only when the agent can write the comment.
	CommentFormat string `json:"commentFormat,omitempty"`
	// RepoVisibility is "private", "internal", "public" or "unknown".
	RepoVisibility string `json:"repoVisibility,omitempty"`
	// FailOn is "breaking" or "affected"; the server validates it.
	FailOn      string `json:"failOn,omitempty"`
	AcceptLabel bool   `json:"acceptLabel"`
}

// Body is the ingest request. Spec is nil when a pull request is closed (no spec needed).
type Body struct {
	Spec        *string           `json:"spec,omitempty"`
	Context     Context           `json:"context"`
	PullRequest *PullRequestHints `json:"pullRequest,omitempty"`
}

// ErrBodyTooLarge: the encoded body is over MaxBodyBytes.
var ErrBodyTooLarge = errors.New("spec too large (limit 10 MB), not sent")

// ErrOverMaxBody: EncodeJSON's result is over MaxBodyBytes. Callers turn it into their own message.
var ErrOverMaxBody = errors.New("encoded body over 10 MB")

// Encode marshals the body exactly as it is sent and enforces the 10 MB limit on the result.
func Encode(b Body) ([]byte, error) {
	data, err := EncodeJSON(b)
	if errors.Is(err, ErrOverMaxBody) {
		return nil, ErrBodyTooLarge
	}
	return data, err
}

// EncodeJSON marshals any request body the way Graphwake's ingest endpoints take it (no HTML escaping, no trailing
// newline) and returns ErrOverMaxBody when the result is over MaxBodyBytes.
func EncodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	data := bytes.TrimRight(buf.Bytes(), "\n")
	if len(data) > MaxBodyBytes {
		return nil, ErrOverMaxBody
	}
	return data, nil
}

// Comment is the pull request comment the server wrote: "upsert" creates or updates it, "resolve" only updates an
// existing one. Key identifies the comment ("graphwake:service:<id>").
type Comment struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Body   string `json:"body"`
}

// Gate is the pipeline decision: Result is "off", "passed", "failed", "accepted" or "invalid"; Message is the short
// sentence to print (empty when the server sent null).
type Gate struct {
	Result  string `json:"result"`
	Message string `json:"message"`
}

// PROutcome is the "pullRequest" object of a successful ingest answer. Comment is nil when no comment format was sent.
type PROutcome struct {
	ChangeSetID string   `json:"changeSetId"`
	ImpactURL   string   `json:"impactUrl"`
	Comment     *Comment `json:"comment"`
	Gate        Gate     `json:"gate"`
}

// Result is the outcome of a send: OK, or the warning to print.
type Result struct {
	OK      bool
	Status  int // 0 when no answer was received
	Warning string
	// PR is the pull request outcome of a successful answer; nil when the server sent none or it could not be read.
	PR *PROutcome
}

// Client calls Graphwake's ingest endpoints with a few retries for network errors and 503.
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
	status, answer, err := c.Do(ctx, http.MethodPost, "/api/ingest/specs", body)
	if err != nil {
		return Result{Warning: "Graphwake unavailable, spec not sent"}
	}
	return Interpret(status, answer)
}

// Do sends one request to BaseURL+path with the client's key and user agent (and a JSON content type when body is
// not nil) under its retry policy: up to Attempts tries on a network error or 503, waiting Backoff and then twice as
// long before each new try. It returns the last answer, or err when no answer was received.
func (c *Client) Do(ctx context.Context, method, path string, body []byte) (status int, answer []byte, err error) {
	endpoint := strings.TrimRight(c.BaseURL, "/") + path
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
		status, answer, err = c.request(ctx, method, endpoint, body)
		transient := err != nil || status == http.StatusServiceUnavailable
		if !transient || attempt >= attempts || ctx.Err() != nil {
			return status, answer, err
		}
		c.Sleep(ctx, wait)
		wait *= 2
	}
}

func (c *Client) request(ctx context.Context, method, endpoint string, body []byte) (int, []byte, error) {
	if c.AttemptTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.AttemptTimeout)
		defer cancel()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	return resp.StatusCode, data, nil
}

// maxResponseBytes caps how much of an answer is read. The answer lists every change before the pull request
// outcome, so a big change set must not cut the outcome off.
const maxResponseBytes = 16 << 20

type okBody struct {
	PullRequest *PROutcome `json:"pullRequest"`
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
		var ok okBody
		if json.Unmarshal(body, &ok) == nil {
			r.PR = ok.PullRequest // an unreadable outcome is treated like none
		}
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
