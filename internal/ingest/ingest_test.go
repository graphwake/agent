package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) {}

func TestInterpret(t *testing.T) {
	tests := []struct {
		status int
		body   string
		ok     bool
		want   string
	}{
		{200, `{"outcome":"Changed","changes":[{"method":"GET"}]}`, true, ""},
		{401, ``, false, "invalid or missing ingest key, spec not sent"},
		{401, `{"code":"invalid_key","error":"x"}`, false, "invalid or missing ingest key, spec not sent"},
		{402, `{"code":"account_closed","error":"closed"}`, false, "the Graphwake account is closed, spec not sent"},
		{409, `{"code":"url_source","error":"..."}`, false, "this service takes its spec from a URL, not from CI; spec not sent"},
		{409, `{"code":"other"}`, false, "unexpected response 409, spec not sent"},
		{413, ``, false, "spec too large (limit 10 MB), not sent"},
		{422, `{"error":"The spec is not valid OpenAPI."}`, false, "spec rejected: The spec is not valid OpenAPI."},
		{422, `not json`, false, "spec rejected: invalid spec"},
		{400, ``, false, "request rejected: bad request"},
		{400, `{"error":"context.kind must be 'live' or 'pullRequest'."}`, false, "request rejected: context.kind must be 'live' or 'pullRequest'."},
		{415, ``, false, "request rejected: bad request"},
		{503, `{"error":"Change detection is temporarily unavailable."}`, false, "Graphwake unavailable, spec not sent"},
		{404, ``, false, "unexpected response 404, spec not sent"},
		{500, `<html>`, false, "unexpected response 500, spec not sent"},
	}
	for _, tt := range tests {
		got := Interpret(tt.status, []byte(tt.body))
		if got.OK != tt.ok || got.Warning != tt.want {
			t.Errorf("Interpret(%d, %q) = %+v, want ok=%v %q", tt.status, tt.body, got, tt.ok, tt.want)
		}
	}
}

func TestEncodeBodyShape(t *testing.T) {
	s := "openapi: 3.0.0 <b>&"
	data, err := Encode(Body{Spec: &s, Context: Context{Kind: "pullRequest", PRID: "42", State: "open",
		Platform: "github", CommitSHA: "abc", CommitTime: "2026-10-30T10:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"spec":"openapi: 3.0.0 <b>&","context":{"kind":"pullRequest","prId":"42","state":"open","platform":"github","commitSha":"abc","commitTime":"2026-10-30T10:00:00Z"}}`
	if string(data) != want {
		t.Fatalf("Encode =\n%s\nwant\n%s", data, want)
	}
	closed, _ := Encode(Body{Context: Context{Kind: "pullRequest", PRID: "42", State: "closed"}})
	if strings.Contains(string(closed), `"spec"`) {
		t.Fatalf("closed PR body carries a spec: %s", closed)
	}
}

// The limit is on the encoded body: JSON escapes make a spec just under 10 MB too large.
func TestEncodeEnforces10MBOnEncodedBody(t *testing.T) {
	ctx := Context{Kind: "live"}
	overhead := len(`{"spec":"","context":{"kind":"live"}}`)

	fits := strings.Repeat("a", MaxBodyBytes-overhead)
	if _, err := Encode(Body{Spec: &fits, Context: ctx}); err != nil {
		t.Fatalf("exactly 10 MB encoded: %v", err)
	}
	over := fits + "a"
	if _, err := Encode(Body{Spec: &over, Context: ctx}); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("10 MB + 1: err = %v", err)
	}
	escaped := strings.Repeat("\n", MaxBodyBytes/2) // 5 MB raw, 10 MB once each newline becomes \n
	if _, err := Encode(Body{Spec: &escaped, Context: ctx}); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("escaped body: err = %v", err)
	}
}

func client(url string) *Client {
	return &Client{BaseURL: url + "/", Key: "dsk_secret", UserAgent: "graphwake-agent/test (github)",
		HTTP: &http.Client{Timeout: time.Second}, Attempts: 3, Backoff: time.Millisecond, Sleep: noSleep}
}

func TestSendPostsWithHeaders(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"outcome":"Baseline","changes":[]}`))
	}))
	defer srv.Close()
	res := client(srv.URL).Send(context.Background(), []byte(`{"context":{"kind":"live"}}`))
	if !res.OK {
		t.Fatalf("Send = %+v", res)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/api/ingest/specs" {
		t.Fatalf("request %s %s", got.Method, got.URL.Path)
	}
	if got.Header.Get("Authorization") != "Bearer dsk_secret" || got.Header.Get("Content-Type") != "application/json" ||
		got.Header.Get("User-Agent") != "graphwake-agent/test (github)" {
		t.Fatalf("headers %v", got.Header)
	}
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) != nil {
		t.Fatalf("body not JSON: %s", body)
	}
}

func TestSendRetries503ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if res := client(srv.URL).Send(context.Background(), []byte(`{}`)); !res.OK || calls.Load() != 2 {
		t.Fatalf("Send = %+v after %d calls", res, calls.Load())
	}
}

func TestSendGivesUpOn503After3Attempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	res := client(srv.URL).Send(context.Background(), []byte(`{}`))
	if res.OK || res.Warning != "Graphwake unavailable, spec not sent" || calls.Load() != 3 {
		t.Fatalf("Send = %+v after %d calls", res, calls.Load())
	}
}

func TestSendDoesNotRetry4xx(t *testing.T) {
	for _, status := range []int{400, 401, 402, 409, 413, 415, 422} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
		}))
		res := client(srv.URL).Send(context.Background(), []byte(`{}`))
		srv.Close()
		if res.OK || calls.Load() != 1 {
			t.Errorf("status %d: %+v after %d calls", status, res, calls.Load())
		}
	}
}

func TestSendNetworkErrorRetriesThenUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	var sleeps int
	c := client(url)
	c.Sleep = func(context.Context, time.Duration) { sleeps++ }
	res := c.Send(context.Background(), []byte(`{}`))
	if res.OK || res.Warning != "Graphwake unavailable, spec not sent" || sleeps != 2 {
		t.Fatalf("Send = %+v with %d waits", res, sleeps)
	}
}

func TestSendBackoffStaysUnder30s(t *testing.T) {
	var waits []time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := client(srv.URL)
	c.Backoff = 2 * time.Second
	c.Sleep = func(_ context.Context, d time.Duration) { waits = append(waits, d) }
	c.Send(context.Background(), []byte(`{}`))
	var total time.Duration
	for _, w := range waits {
		total += w
	}
	if len(waits) != 2 || waits[1] != 2*waits[0] || total > 30*time.Second {
		t.Fatalf("waits %v", waits)
	}
}

// A hanging server: every attempt is cut by AttemptTimeout and the whole send by Deadline.
func TestSendRespectsAttemptTimeoutAndDeadline(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := client(srv.URL)
	c.HTTP = &http.Client{} // no client timeout: only the agent's own caps apply
	c.AttemptTimeout = 100 * time.Millisecond
	c.Deadline = 250 * time.Millisecond
	start := time.Now()
	res := c.Send(context.Background(), []byte(`{}`))
	elapsed := time.Since(start)
	if res.OK || res.Warning != "Graphwake unavailable, spec not sent" {
		t.Fatalf("Send = %+v", res)
	}
	if elapsed > time.Second || calls.Load() > 3 {
		t.Fatalf("took %v with %d calls; the caps were not applied", elapsed, calls.Load())
	}
}

func TestInterpretDecodesPullRequestOutcome(t *testing.T) {
	body := `{"outcome":"Changed","changes":[],"pullRequest":{"changeSetId":"6f1c0d2e-0000-4000-8000-000000000001",` +
		`"impactUrl":"https://app.graphwake.com/impact/6f1c","comment":{"action":"upsert","key":"graphwake:service:42",` +
		`"body":"## Graphwake\n- breaks **payments**"},"gate":{"result":"failed","message":"This PR breaks 2 teams."}}}`
	got := Interpret(200, []byte(body))
	if !got.OK || got.Warning != "" {
		t.Fatalf("Interpret = %+v", got)
	}
	want := PROutcome{
		ChangeSetID: "6f1c0d2e-0000-4000-8000-000000000001",
		ImpactURL:   "https://app.graphwake.com/impact/6f1c",
		Comment:     &Comment{Action: "upsert", Key: "graphwake:service:42", Body: "## Graphwake\n- breaks **payments**"},
		Gate:        Gate{Result: "failed", Message: "This PR breaks 2 teams."},
	}
	if got.PR == nil || got.PR.ChangeSetID != want.ChangeSetID || got.PR.ImpactURL != want.ImpactURL ||
		got.PR.Comment == nil || *got.PR.Comment != *want.Comment || got.PR.Gate != want.Gate {
		t.Fatalf("PR = %+v (comment %+v), want %+v", got.PR, got.PR.Comment, want)
	}

	// No comment (commentFormat was not sent) and a null gate message.
	noComment := `{"pullRequest":{"changeSetId":"x","impactUrl":"u","comment":null,"gate":{"result":"off","message":null}}}`
	got = Interpret(200, []byte(noComment))
	if !got.OK || got.PR == nil || got.PR.Comment != nil || got.PR.Gate != (Gate{Result: "off"}) {
		t.Fatalf("Interpret(no comment) = %+v PR %+v", got, got.PR)
	}
}

func TestInterpretWithoutPullRequestLeavesNil(t *testing.T) {
	for _, body := range []string{``, `{"outcome":"Changed","changes":[]}`, `not json`, `{"pullRequest":"oops"}`, `{"pullRequest":null}`} {
		got := Interpret(200, []byte(body))
		if !got.OK || got.PR != nil {
			t.Errorf("Interpret(200, %q) = %+v PR %+v, want OK with no PR", body, got, got.PR)
		}
	}
	// Error answers never carry an outcome.
	if got := Interpret(422, []byte(`{"error":"bad","pullRequest":{"gate":{"result":"failed"}}}`)); got.PR != nil {
		t.Fatalf("Interpret(422) PR = %+v", got.PR)
	}
}

func TestEncodeOmitsEmptyHints(t *testing.T) {
	ctx := Context{Kind: "pullRequest", PRID: "7", State: "open"}
	data, err := Encode(Body{Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "pullRequest") != 1 { // only context.kind
		t.Fatalf("body without hints carries a pullRequest field: %s", data)
	}

	data, err = Encode(Body{Context: ctx, PullRequest: &PullRequestHints{RepoVisibility: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"context":{"kind":"pullRequest","prId":"7","state":"open"},"pullRequest":{"repoVisibility":"unknown","acceptLabel":false}}`
	if string(data) != want {
		t.Fatalf("Encode =\n%s\nwant\n%s", data, want)
	}

	data, _ = Encode(Body{Context: ctx, PullRequest: &PullRequestHints{CommentFormat: "github", RepoVisibility: "private",
		FailOn: "breaking", AcceptLabel: true}})
	want = `{"context":{"kind":"pullRequest","prId":"7","state":"open"},"pullRequest":{"commentFormat":"github","repoVisibility":"private","failOn":"breaking","acceptLabel":true}}`
	if string(data) != want {
		t.Fatalf("Encode =\n%s\nwant\n%s", data, want)
	}
}

// An outcome with a long comment body still decodes: the response cap is above 64 KiB.
func TestSendReadsLargePullRequestOutcome(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"pullRequest":{"changeSetId":"c","impactUrl":"u","comment":{"action":"upsert","key":"k","body":"` +
			long + `"},"gate":{"result":"passed","message":null}}}`))
	}))
	defer srv.Close()
	res := client(srv.URL).Send(context.Background(), []byte(`{}`))
	if !res.OK || res.PR == nil || res.PR.Comment == nil || len(res.PR.Comment.Body) != len(long) {
		t.Fatalf("Send = %+v PR %+v", res, res.PR)
	}
}

// A big change set puts more than 512 KiB of changes[] before pullRequest; the outcome at the end still decodes.
func TestSendReadsPullRequestAfterLargeChanges(t *testing.T) {
	change := `{"kind":"breaking","path":"/v1/orders/{}","detail":"` + strings.Repeat("y", 1000) + `"}`
	changes := strings.Repeat(change+",", 700) + change // ~700 KiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"changes":[` + changes + `],"pullRequest":{"changeSetId":"c","impactUrl":"u",` +
			`"comment":{"action":"upsert","key":"k","body":"b"},"gate":{"result":"failed","message":"m"}}}`))
	}))
	defer srv.Close()
	res := client(srv.URL).Send(context.Background(), []byte(`{}`))
	if !res.OK || res.PR == nil || res.PR.Gate.Result != "failed" {
		t.Fatalf("Send = %+v PR %+v", res, res.PR)
	}
}
