package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ddClock is the clock the datadog tests read: 23:30 on 18 November in New York is 19 November in UTC.
func ddClock() time.Time {
	return time.Date(2026, 11, 18, 23, 30, 0, 0, time.FixedZone("EST", -5*3600))
}

// ddParse runs the datadog preset with ddClock and collects what it emits.
func ddParse(t *testing.T, input string, opts ParseOptions) ([]Record, ParseResult, error) {
	t.Helper()
	if opts.Now == nil {
		opts.Now = ddClock
	}
	return parseResult(t, "datadog", input, opts)
}

// ddAnswer is one aggregate answer with the given buckets and meta ("" = no meta member).
func ddAnswer(meta string, buckets ...string) string {
	if meta != "" {
		meta = `,"meta":` + meta
	}
	return `{"data":[` + strings.Join(buckets, ",") + `]` + meta + `}`
}

// ddBucket is one bucket with the given "by" members and count.
func ddBucket(by, count string) string {
	return `{"type":"bucket","attributes":{"by":{` + by + `},"compute":{"c0":` + count + `}}}`
}

const ddDone = `{"elapsed":12,"status":"done"}`

func readTestdata(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var ddFixtureRecords = []Record{
	{Caller: "checkout-web", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/8812", Count: 42, Day: "2026-11-19"},
	{Caller: "checkout-web", Kind: "service", Method: "POST", Host: "orders.internal", Path: "/v1/orders", Count: 7, Day: "2026-11-19"},
	{Caller: "checkout-web", Kind: "service", Method: "DELETE", Host: "inventory.internal", Path: "/items/{id}", Count: 3, Day: "2026-11-19"},
}

func TestDatadog_buckets_to_records(t *testing.T) {
	got, res, err := ddParse(t, readTestdata(t, "datadog-aggregate.json"), ParseOptions{})
	if err != nil || res != (ParseResult{}) {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	assertRecords(t, got, ddFixtureRecords)

	// A bucket without a service is anonymous; a count written as a whole float is read.
	got, res, err = ddParse(t, ddAnswer(ddDone,
		ddBucket(`"@http.url_details.host":"orders.internal","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "2"),
		ddBucket(`"service":null,"@http.url_details.host":"orders.internal","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "5.0"),
	), ParseOptions{})
	if err != nil || res.Skipped != 0 || len(got) != 2 || got[0].Caller != "" || got[1].Caller != "" || got[1].Count != 5 {
		t.Fatalf("records = %v, result = %+v, err = %v", got, res, err)
	}

	// --caller-field names another facet of "by".
	got, _, err = ddParse(t, ddAnswer(ddDone,
		ddBucket(`"service":"checkout-web","@peer.service":"checkout-frontend","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "1"),
	), ParseOptions{CallerField: "@peer.service"})
	if err != nil || len(got) != 1 || got[0].Caller != "checkout-frontend" {
		t.Fatalf("caller field: records = %v, err = %v", got, err)
	}
}

func TestDatadog_host_and_path_fallbacks(t *testing.T) {
	got, res, err := ddParse(t, ddAnswer(ddDone,
		// The host: @http.url_details.host, else @out.host, else @peer.hostname.
		ddBucket(`"service":"a","@http.url_details.host":"orders.internal","@out.host":"x.example","@peer.hostname":"y.example","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "1"),
		ddBucket(`"service":"b","@out.host":"Orders.Internal:8080","@peer.hostname":"y.example","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "1"),
		ddBucket(`"service":"c","@peer.hostname":"orders.internal","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "1"),
		ddBucket(`"service":"d","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "1"),
		// The path: @http.url_details.path, else resource_name when it starts with a method and a space.
		ddBucket(`"service":"e","@http.url_details.host":"orders.internal","resource_name":"GET /v1/orders/{id}","@http.method":"GET"`, "1"),
		ddBucket(`"service":"f","@http.url_details.host":"orders.internal","resource_name":"post /v1/orders","@http.method":"POST"`, "1"),
		// The method comes from @http.method only, never from resource_name: skipped.
		ddBucket(`"service":"j","@http.url_details.host":"orders.internal","resource_name":"GET /v1/orders"`, "1"),
		// A resource name that is not "METHOD /path", and no path: skipped.
		ddBucket(`"service":"g","resource_name":"orders.get","@http.method":"GET"`, "1"),
		ddBucket(`"service":"h","resource_name":"FETCH /v1/orders","@http.method":"GET"`, "1"),
		// No method anywhere: skipped.
		ddBucket(`"service":"i","@http.url_details.path":"/v1/orders/1"`, "1"),
	), ParseOptions{})
	if err != nil || res.Skipped != 4 {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	assertRecords(t, got, []Record{
		{Caller: "a", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-19"},
		{Caller: "b", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-19"},
		{Caller: "c", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-19"},
		{Caller: "d", Kind: "service", Method: "GET", Host: "", Path: "/v1/orders/1", Count: 1, Day: "2026-11-19"},
		{Caller: "e", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/{id}", Count: 1, Day: "2026-11-19"},
		{Caller: "f", Kind: "service", Method: "POST", Host: "orders.internal", Path: "/v1/orders", Count: 1, Day: "2026-11-19"},
	})
}

// Datadog may hold a concrete path or a template (its own quantized "?" included): the preset passes either on as it
// is. Cutting "/orders/?" at the "?" would make it match the list endpoint instead of the item endpoint.
func TestDatadog_concrete_and_template_paths_pass_through(t *testing.T) {
	paths := []string{"/v1/orders/8812", "/v1/orders/{id}", "/v1/orders/{}", "/v1/orders/?", "/v1/orders/?/lines/?"}
	var buckets []string
	for _, p := range paths {
		buckets = append(buckets, ddBucket(`"service":"checkout-web","@http.url_details.path":"`+p+`","@http.method":"GET"`, "1"))
	}
	got, res, err := ddParse(t, ddAnswer(ddDone, buckets...), ParseOptions{})
	if err != nil || res.Skipped != 0 || len(got) != len(paths) {
		t.Fatalf("records = %v, result = %+v, err = %v", got, res, err)
	}
	for i, p := range paths {
		if got[i].Path != p {
			t.Errorf("path %d = %q, want %q", i, got[i].Path, p)
		}
	}

	// A template matches the endpoint it names, a quantized "?" the item endpoint.
	m, err := NewMatcher(Templates{Services: []Service{{ID: "svc-orders", Name: "orders", ServerURLs: []string{"https://orders.internal/v1"},
		Endpoints: []Endpoint{{ID: "ep-get", Method: "GET", Path: "/orders/{orderId}"}, {ID: "ep-list", Method: "GET", Path: "/orders"}}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got[:4] {
		r.Host = "orders.internal"
		if e, _, ok := m.Match(r); !ok || e.ID != "ep-get" {
			t.Errorf("%s matched %q, %v; want ep-get", r.Path, e.ID, ok)
		}
	}
}

// The documented workflow appends one answer per calling service to one file: curl writes no newline, so the
// answers follow one another on one line; they may also be on separate lines or pretty-printed.
func TestDatadog_several_answers_in_one_file(t *testing.T) {
	one := strings.TrimSpace(readTestdata(t, "datadog-aggregate.json"))
	two := ddAnswer(ddDone, ddBucket(`"service":"billing-worker","@http.url_details.host":"orders.internal","@http.url_details.path":"/v1/orders","@http.method":"GET"`, "4"))
	pretty := "{\n  \"data\": [\n    {\n      \"type\": \"bucket\",\n      \"attributes\": {\n        \"by\": {\n" +
		"          \"service\": \"billing-worker\",\n          \"@http.url_details.host\": \"orders.internal\",\n" +
		"          \"@http.url_details.path\": \"/v1/orders\",\n          \"@http.method\": \"GET\"\n        },\n" +
		"        \"compute\": {\n          \"c0\": 4\n        }\n      }\n    }\n  ],\n  \"meta\": {\n    \"status\": \"done\"\n  }\n}\n"
	billing := Record{Caller: "billing-worker", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders", Count: 4, Day: "2026-11-19"}
	want := append(append([]Record{}, ddFixtureRecords...), billing)
	for name, in := range map[string]string{
		"one after another": one + two,
		"on separate lines": one + "\n" + two + "\n",
		"blank lines":       "\n" + one + "\n\n\r\n" + two,
		"pretty-printed":    one + "\n" + pretty,
		"space between":     one + "  " + two,
	} {
		got, res, err := ddParse(t, in, ParseOptions{})
		if err != nil || res != (ParseResult{}) {
			t.Fatalf("%s: result = %+v, err = %v", name, res, err)
		}
		assertRecords(t, got, want)
	}
}

func TestDatadog_timeout_or_warnings_set_partial(t *testing.T) {
	bucket := ddBucket(`"service":"checkout-web","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "1")
	cases := []struct {
		name, input string
		partial     bool
	}{
		{"done", ddAnswer(ddDone, bucket), false},
		{"done, empty warnings", ddAnswer(`{"status":"done","warnings":[]}`, bucket), false},
		{"timeout", ddAnswer(`{"status":"timeout"}`, bucket), true},
		{"warnings", ddAnswer(`{"status":"done","warnings":[{"code":"unknown_index","detail":"x","title":"y"}]}`, bucket), true},
		// No meta, or no status: the tool reported nothing, so nothing is said.
		{"no meta", ddAnswer("", bucket), false},
		{"no status", ddAnswer(`{"elapsed":3}`, bucket), false},
		// meta before data.
		{"meta first", `{"meta":{"status":"timeout"},"data":[` + bucket + `]}`, true},
		// One incomplete answer among complete ones.
		{"one of several", ddAnswer(ddDone, bucket) + ddAnswer(`{"status":"timeout"}`, bucket) + ddAnswer(ddDone, bucket), true},
	}
	for _, c := range cases {
		got, res, err := ddParse(t, c.input, ParseOptions{})
		if err != nil || res.Skipped != 0 || res.Partial != c.partial {
			t.Errorf("%s: result = %+v, err = %v; want partial %v", c.name, res, err, c.partial)
		}
		// The buckets present are still read.
		if want := strings.Count(c.input, `"bucket"`); len(got) != want {
			t.Errorf("%s: %d records, want %d", c.name, len(got), want)
		}
	}
}

func TestDatadog_day_comes_from_the_clock(t *testing.T) {
	in := ddAnswer(ddDone, ddBucket(`"service":"checkout-web","@http.url_details.path":"/v1/orders/1","@http.method":"GET"`, "1"))
	got, _, err := ddParse(t, in, ParseOptions{})
	if err != nil || len(got) != 1 || got[0].Day != "2026-11-19" {
		t.Fatalf("records = %v, err = %v; want day 2026-11-19 (the clock's day in UTC)", got, err)
	}

	// Without a clock, today in UTC.
	before := time.Now().UTC().Format(dayLayout)
	got, _, err = parseResult(t, "datadog", in, ParseOptions{})
	after := time.Now().UTC().Format(dayLayout)
	if err != nil || len(got) != 1 || got[0].Day != before && got[0].Day != after {
		t.Fatalf("records = %v, err = %v; want today (%s)", got, err, after)
	}

	// The clock is read once per run.
	calls := 0
	clock := func() time.Time { calls++; return ddClock() }
	if _, _, err := ddParse(t, in+in+in, ParseOptions{Now: clock}); err != nil || calls != 1 {
		t.Errorf("clock read %d times, err = %v; want once", calls, err)
	}
}
