package usage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) error { return nil }

func testClient(url string) *Client {
	return &Client{BaseURL: url + "/", Key: "gwu_secret", UserAgent: "graphwake-agent/test (other)",
		HTTP: &http.Client{Timeout: time.Second}, Sleep: noSleep}
}

// answer serves one fixed status and body and counts the calls.
func answer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestClient_status_to_result(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"code":"invalid_key","error":"x"}`, "usage not sent: the key was not accepted"},
		{402, `{"code":"account_closed","error":"x"}`, "usage not sent: the Graphwake account is closed"},
		{403, `{"code":"usage_not_in_plan","error":"x"}`, "usage not sent: Usage Discovery is not in your plan"},
		{400, `{"code":"invalid_usage_snapshot","error":"rows[3].endpointId is unknown"}`, "usage not sent: Graphwake rejected the summary as invalid"},
		{413, `{"code":"usage_snapshot_too_large","error":"x"}`, "usage not sent: the summary is too large"},
		{422, `{"code":"usage_too_many_callers","error":"x"}`, "usage not sent: too many distinct callers. The caller field may identify end users; choose another with --caller-field"},
		{422, `{"code":"usage_source_callers_full","error":"SERVER-TEXT"}`, sourceCallersFull},
		{422, ``, "usage not sent: too many distinct callers. The caller field may identify end users; choose another with --caller-field"},
		{422, `<html>usage_source_callers_full`, "usage not sent: too many distinct callers. The caller field may identify end users; choose another with --caller-field"},
		{422, `{"code":"usage_source_callers_full"` + strings.Repeat(" ", 70<<10) + `}`, "usage not sent: too many distinct callers. The caller field may identify end users; choose another with --caller-field"},
		{422, `{"code":"something_new","error":"x"}`, "usage not sent: too many distinct callers. The caller field may identify end users; choose another with --caller-field"},
		{429, `{"code":"rate_limited","error":"x"}`, "usage not sent: Graphwake is rate limiting this key; try again later"},
		{503, ``, "usage not sent: Graphwake could not be reached"},
		{500, `<html>`, "usage not sent: unexpected response 500"},
		{404, ``, "usage not sent: unexpected response 404"},
	}
	for _, tt := range tests {
		srv, _ := answer(t, tt.status, tt.body)
		c := testClient(srv.URL)
		if res := c.Send(context.Background(), []byte(`{}`)); res.OK || res.Status != tt.status || res.Warning != tt.want {
			t.Errorf("Send %d: %+v, want %q", tt.status, res, tt.want)
		}
		if _, res := c.Templates(context.Background()); res.OK || res.Warning != tt.want {
			t.Errorf("Templates %d: %+v, want %q", tt.status, res, tt.want)
		}
	}
}

func TestClient_send_success_line(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{`{"callers":18,"newCallers":3,"rows":214,"skippedRows":0,"suggestions":2,"subscriptionsAdded":0}`,
			"usage sent: 18 callers, 214 endpoint rows (3 new callers, 2 suggestions, 0 subscriptions added)."},
		{`{"callers":1,"newCallers":1,"rows":1,"skippedRows":0,"suggestions":1,"subscriptionsAdded":1}`,
			"usage sent: 1 caller, 1 endpoint row (1 new caller, 1 suggestion, 1 subscription added)."},
		{`not json`, "usage sent."},
	}
	for _, tt := range tests {
		srv, _ := answer(t, 200, tt.body)
		res := testClient(srv.URL).Send(context.Background(), []byte(`{}`))
		if !res.OK || res.Info != tt.want || res.Warning != "" || res.SkippedRows != 0 {
			t.Errorf("Send(%s) = %+v, want %q", tt.body, res, tt.want)
		}
	}
	srv, _ := answer(t, 200, `{"callers":1,"rows":2,"skippedRows":3}`)
	if res := testClient(srv.URL).Send(context.Background(), []byte(`{}`)); res.SkippedRows != 3 {
		t.Errorf("SkippedRows = %d, want 3", res.SkippedRows)
	}
}

func TestClient_requests_and_headers(t *testing.T) {
	type seen struct {
		method, path, auth, agent, contentType, body string
	}
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("User-Agent"),
			r.Header.Get("Content-Type"), string(body)})
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"source":{"id":"src","name":"gateway","preset":"apim"},"limits":{"maxCallers":5},
				"services":[{"id":"s1","name":"orders","endpoints":[{"id":"e1","method":"GET","path":"/orders/{}"}]}]}`)
			return
		}
		io.WriteString(w, `{"callers":1}`)
	}))
	defer srv.Close()
	c := testClient(srv.URL)

	tpl, res := c.Templates(context.Background())
	if !res.OK || res.Status != 200 || tpl.Source.Name != "gateway" || tpl.Limits.MaxCallers != 5 ||
		len(tpl.Services) != 1 || tpl.Services[0].Endpoints[0].ID != "e1" {
		t.Fatalf("Templates = %+v, %+v", tpl, res)
	}
	if res := c.Send(context.Background(), []byte(`{"preset":"apim"}`)); !res.OK {
		t.Fatalf("Send = %+v", res)
	}
	want := []seen{
		{"GET", "/api/ingest/usage/templates", "Bearer gwu_secret", "graphwake-agent/test (other)", "", ""},
		{"POST", "/api/ingest/usage", "Bearer gwu_secret", "graphwake-agent/test (other)", "application/json", `{"preset":"apim"}`},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("requests =\n%+v\nwant\n%+v", got, want)
	}
}

func TestClient_unreadable_templates(t *testing.T) {
	srv, _ := answer(t, 200, `<html>`)
	if _, res := testClient(srv.URL).Templates(context.Background()); res.OK ||
		res.Warning != "usage not sent: Graphwake's endpoint list could not be read" {
		t.Fatalf("Templates = %+v", res)
	}
}

func TestClient_retries_503_then_succeeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1)%2 == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := testClient(srv.URL)
	if _, res := c.Templates(context.Background()); !res.OK || calls.Load() != 2 {
		t.Fatalf("Templates = %+v after %d calls", res, calls.Load())
	}
	if res := c.Send(context.Background(), []byte(`{}`)); !res.OK || calls.Load() != 4 {
		t.Fatalf("Send = %+v after %d calls", res, calls.Load())
	}
}

func TestClient_gives_up_on_503_after_3_attempts(t *testing.T) {
	srv, calls := answer(t, http.StatusServiceUnavailable, ``)
	var waits []time.Duration
	c := testClient(srv.URL)
	c.Sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	res := c.Send(context.Background(), []byte(`{}`))
	if res.OK || res.Warning != "usage not sent: Graphwake could not be reached" || calls.Load() != 3 {
		t.Fatalf("Send = %+v after %d calls", res, calls.Load())
	}
	if len(waits) != 2 || waits[0] != 2*time.Second || waits[1] != 4*time.Second {
		t.Fatalf("waits = %v, want [2s 4s]", waits)
	}
}

func TestClient_never_retries_4xx(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 422, 429} {
		srv, calls := answer(t, status, `{}`)
		c := testClient(srv.URL)
		c.Send(context.Background(), []byte(`{}`))
		c.Templates(context.Background())
		if calls.Load() != 2 {
			t.Errorf("%d: %d calls, want 1 per request", status, calls.Load())
		}
	}
}

func TestClient_unreachable(t *testing.T) {
	srv, _ := answer(t, 200, `{}`)
	srv.Close()
	c := testClient(srv.URL)
	if res := c.Send(context.Background(), []byte(`{}`)); res.OK || res.Status != 0 ||
		res.Warning != "usage not sent: Graphwake could not be reached" {
		t.Fatalf("Send = %+v", res)
	}
	if _, res := c.Templates(context.Background()); res.OK || res.Warning != "usage not sent: Graphwake could not be reached" {
		t.Fatalf("Templates = %+v", res)
	}
}

// The policy is the spec upload's: 3 attempts of at most 8 s with waits of 2 s and 4 s fit in the 30 s per call.
func TestClient_retry_budget(t *testing.T) {
	worst := attempts*attemptTimeout + backoff + 2*backoff
	if attempts != 3 || backoff != 2*time.Second || attemptTimeout != 8*time.Second || callDeadline != 30*time.Second ||
		worst > callDeadline {
		t.Fatalf("attempts %d, backoff %v, attempt %v, deadline %v, worst case %v", attempts, backoff, attemptTimeout,
			callDeadline, worst)
	}
}

// The success line never repeats server text other than the counts.
func TestClient_success_ignores_unknown_members(t *testing.T) {
	srv, _ := answer(t, 200, `{"callers":2,"message":"`+strings.Repeat("x", 10)+`"}`)
	res := testClient(srv.URL).Send(context.Background(), []byte(`{}`))
	if !res.OK || strings.Contains(res.Info, "xxxx") {
		t.Fatalf("Send = %+v", res)
	}
}

// A redirect is never followed, whatever the http.Client given: neither the templates request nor the summary
// reaches the address a 3xx points to, and the answer is reported as an unexpected response.
func TestClient_never_follows_a_redirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		io.WriteString(w, `{"services":[]}`)
	}))
	t.Cleanup(other.Close)
	for _, status := range []int{301, 302, 303, 307, 308} {
		first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+r.URL.Path, status)
		}))
		c := testClient(first.URL)
		c.HTTP = &http.Client{Timeout: time.Second} // follows redirects unless the usage client stops it
		want := fmt.Sprintf("usage not sent: unexpected response %d", status)
		if _, res := c.Templates(context.Background()); res.OK || res.Status != status || res.Warning != want {
			t.Errorf("Templates %d: %+v, want %q", status, res, want)
		}
		if res := c.Send(context.Background(), []byte(`{"rows":[]}`)); res.OK || res.Status != status || res.Warning != want {
			t.Errorf("Send %d: %+v, want %q", status, res, want)
		}
		first.Close()
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the redirect target received %d requests", n)
	}
}

// sourceCallersFull is the warning for a 422 usage_source_callers_full: the source, not the summary, holds too many callers.
const sourceCallersFull = "usage not sent: this source already holds too many callers (claimed and ignored callers count). " +
	"Unclaim or stop ignoring callers you no longer need in Graphwake, or add a new usage source."
