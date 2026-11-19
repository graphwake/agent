package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/graphwake/agent/internal/ingest"
)

// The retry policy of both usage calls, the spec upload's: 3 attempts on a network error or 503, waiting 2 s and
// then 4 s, at most 8 s per attempt and 30 s per call in all.
const (
	attempts       = 3
	backoff        = 2 * time.Second
	attemptTimeout = 8 * time.Second
	callDeadline   = 30 * time.Second
)

// Client calls Graphwake's usage endpoints with the usage key. It never returns an error to fail the pipeline with:
// every outcome is a Result.
type Client struct {
	BaseURL   string
	Key       string
	UserAgent string
	HTTP      *http.Client
	// Sleep waits between attempts; it returns early with ctx's error when ctx ends. nil waits on a timer.
	Sleep func(context.Context, time.Duration) error
}

// Result is the outcome of a call: OK with the line to print (Info, Send only), or the warning to print. Both are
// complete sentences without the "graphwake: " prefix.
type Result struct {
	OK      bool
	Status  int // 0 when no answer was received
	Warning string
	Info    string
}

// Templates fetches the endpoint templates, limits and mappings of the key's usage source.
func (c *Client) Templates(ctx context.Context) (Templates, Result) {
	var t Templates
	res, answer := c.call(ctx, http.MethodGet, "/api/ingest/usage/templates", nil)
	if res.OK && json.Unmarshal(answer, &t) != nil {
		return Templates{}, Result{Status: res.Status, Warning: "usage not sent: Graphwake's endpoint list could not be read"}
	}
	return t, res
}

// sendAnswer is the success answer of POST /api/ingest/usage.
type sendAnswer struct {
	Callers            int `json:"callers"`
	NewCallers         int `json:"newCallers"`
	Rows               int `json:"rows"`
	SkippedRows        int `json:"skippedRows"`
	Suggestions        int `json:"suggestions"`
	SubscriptionsAdded int `json:"subscriptionsAdded"`
}

// Send posts an encoded snapshot (from Encode).
func (c *Client) Send(ctx context.Context, body []byte) Result {
	res, answer := c.call(ctx, http.MethodPost, "/api/ingest/usage", body)
	if !res.OK {
		return res
	}
	var a sendAnswer
	if json.Unmarshal(answer, &a) != nil {
		res.Info = "usage sent."
		return res
	}
	res.Info = fmt.Sprintf("usage sent: %s, %s (%s, %s, %s added).",
		Plural(int64(a.Callers), "caller", "callers"), Plural(int64(a.Rows), "endpoint row", "endpoint rows"),
		Plural(int64(a.NewCallers), "new caller", "new callers"), Plural(int64(a.Suggestions), "suggestion", "suggestions"),
		Plural(int64(a.SubscriptionsAdded), "subscription", "subscriptions"))
	return res
}

// Plural writes a count with the singular or the plural noun: "1 caller", "2 callers".
func Plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// call sends one request with the upload's retry policy and maps a failure to its warning. answer is the body of a
// 2xx answer.
func (c *Client) call(ctx context.Context, method, path string, body []byte) (Result, []byte) {
	sleep := c.Sleep
	if sleep == nil {
		sleep = wait
	}
	t := ingest.Client{
		BaseURL:        c.BaseURL,
		Key:            c.Key,
		UserAgent:      c.UserAgent,
		HTTP:           c.HTTP,
		Attempts:       attempts,
		Backoff:        backoff,
		Sleep:          func(ctx context.Context, d time.Duration) { _ = sleep(ctx, d) },
		Deadline:       callDeadline,
		AttemptTimeout: attemptTimeout,
	}
	status, answer, err := t.Do(ctx, method, path, body)
	if err != nil {
		return Result{Warning: unreachable}, nil
	}
	return interpret(status), answer
}

const unreachable = "usage not sent: Graphwake could not be reached"

// interpret maps a status to a Result. Server texts are never printed: every warning is a fixed sentence.
func interpret(status int) Result {
	r := Result{Status: status}
	switch {
	case status >= 200 && status <= 299:
		r.OK = true
	case status == http.StatusUnauthorized:
		r.Warning = "usage not sent: the key was not accepted"
	case status == http.StatusPaymentRequired:
		r.Warning = "usage not sent: the Graphwake account is closed"
	case status == http.StatusForbidden:
		r.Warning = "usage not sent: Usage Discovery is not in your plan"
	case status == http.StatusBadRequest:
		r.Warning = "usage not sent: Graphwake rejected the summary as invalid"
	case status == http.StatusRequestEntityTooLarge:
		r.Warning = "usage not sent: the summary is too large"
	case status == http.StatusUnprocessableEntity:
		r.Warning = "usage not sent: too many distinct callers. The caller field may identify end users; choose another with --caller-field"
	case status == http.StatusTooManyRequests:
		r.Warning = "usage not sent: Graphwake is rate limiting this key; try again later"
	case status == http.StatusServiceUnavailable:
		r.Warning = unreachable
	default:
		r.Warning = fmt.Sprintf("usage not sent: unexpected response %d", status)
	}
	return r
}

// wait is the Sleep of a Client that has none.
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
