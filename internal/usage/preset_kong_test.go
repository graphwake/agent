package usage

import (
	"fmt"
	"strings"
	"testing"
)

func TestKong_file_log_lines(t *testing.T) {
	assertRecords(t, parseFixture(t, "kong", "kong-file-log.jsonl", ParseOptions{}), []Record{
		// consumer.username; the host header loses its port and the uri its query string.
		{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "gateway.example", Path: "/orders/v1/orders/8812", Count: 1, Day: "2026-11-18"},
		// Only custom_id; no host header, so the host of request.url.
		{Caller: "billing-worker", Kind: "client", Method: "POST", Host: "billing.example", Path: "/billing/invoices/41/lines/3", Count: 1, Day: "2026-11-17"},
		// No consumer: anonymous.
		{Caller: "", Kind: "client", Method: "DELETE", Host: "gateway.example", Path: "/orders/v1/orders/9", Count: 1, Day: "2026-11-16"},
	})
}

func TestKong_http_log_batch_array(t *testing.T) {
	assertRecords(t, parseFixture(t, "kong", "kong-http-log-batch.json", ParseOptions{}), []Record{
		{Caller: "partner-portal", Kind: "client", Method: "GET", Host: "api.example", Path: "/inventory/items/77", Count: 1, Day: "2026-11-18"},
		{Caller: "", Kind: "client", Method: "POST", Host: "api.example", Path: "/inventory/items", Count: 1, Day: "2026-11-15"},
	})
}

// kongLine is one file-log line with the given consumer member ("" = none) and extra request headers.
func kongLine(consumer, headers string) string {
	if consumer != "" {
		consumer = `"consumer":` + consumer + `,`
	}
	return `{` + consumer + `"request":{"headers":{"host":"gateway.example"` + headers + `},"uri":"/orders/1","method":"GET","querystring":{},"url":"http://gateway.example/orders/1"},"started_at":1794993164000}`
}

func TestKong_caller_precedence(t *testing.T) {
	cases := []struct{ consumer, want string }{
		{`{"id":"c-id","username":"checkout-web","custom_id":"cw-001"}`, "checkout-web"},
		{`{"id":"c-id","custom_id":"cw-001"}`, "cw-001"},
		{`{"id":"c-id","username":"","custom_id":"cw-001"}`, "cw-001"},
		{`{"id":"c-id"}`, "c-id"},
		{`{"id":"c-id","username":null,"custom_id":""}`, "c-id"},
	}
	for _, c := range cases {
		got, skipped, err := parse(t, "kong", kongLine(c.consumer, ""), ParseOptions{})
		if err != nil || skipped != 0 || len(got) != 1 || got[0].Caller != c.want {
			t.Errorf("consumer %s: records = %v, skipped = %d, err = %v; want caller %q", c.consumer, got, skipped, err, c.want)
		}
	}
}

func TestKong_anonymous_without_consumer(t *testing.T) {
	for _, consumer := range []string{"", "null", "{}"} {
		got, skipped, err := parse(t, "kong", kongLine(consumer, ""), ParseOptions{})
		if err != nil || skipped != 0 || len(got) != 1 || got[0].Caller != "" {
			t.Errorf("consumer %q: records = %v, skipped = %d, err = %v; want one anonymous record", consumer, got, skipped, err)
		}
	}
}

func TestKong_strips_query_and_port(t *testing.T) {
	in := `{"request":{"headers":{"host":"Gateway.Example:8443"},"uri":"/orders/1?token=abc&x=1#frag","method":"get","url":"https://gateway.example:8443/orders/1?token=abc"},"started_at":1794993164000}` + "\n" +
		// No host header: the host of request.url, without its port.
		`{"request":{"headers":{},"uri":"/orders/2?token=abc","method":"GET","url":"https://Orders.Example:9443/orders/2?token=abc"},"started_at":1794993164000}`
	got, skipped, err := parse(t, "kong", in, ParseOptions{})
	if err != nil || skipped != 0 {
		t.Fatalf("skipped = %d, err = %v", skipped, err)
	}
	assertRecords(t, got, []Record{
		{Kind: "client", Method: "GET", Host: "gateway.example", Path: "/orders/1", Count: 1, Day: "2026-11-18"},
		{Kind: "client", Method: "GET", Host: "orders.example", Path: "/orders/2", Count: 1, Day: "2026-11-18"},
	})
}

func TestKong_caller_field_dotted_path_and_header(t *testing.T) {
	line := kongLine(`{"id":"c-id","username":"checkout-web","custom_id":"cw-001"}`, `,"X-Client-Id":"mobile-app"`)
	cases := []struct{ field, want string }{
		{"consumer.custom_id", "cw-001"},
		{"consumer.id", "c-id"},
		{"request.headers.x-client-id", "mobile-app"}, // header names ignore case
		{"request.headers.X-CLIENT-ID", "mobile-app"},
		{"request.headers.x-missing", ""}, // a missing member is anonymous
	}
	for _, c := range cases {
		got, skipped, err := parse(t, "kong", line, ParseOptions{CallerField: c.field})
		if err != nil || skipped != 0 || len(got) != 1 || got[0].Caller != c.want {
			t.Errorf("--caller-field %s: records = %v, skipped = %d, err = %v; want caller %q", c.field, got, skipped, err, c.want)
		}
	}

	// A repeated header is logged as an array: its first value is the caller.
	got, _, err := parse(t, "kong", kongLine("", `,"x-client-id":["mobile-app","web"]`), ParseOptions{CallerField: "request.headers.x-client-id"})
	if err != nil || len(got) != 1 || got[0].Caller != "mobile-app" {
		t.Errorf("repeated header: records = %v, err = %v", got, err)
	}

	// A caller field that names an object is not a caller name: the line is skipped, as a non-text field is in apim.
	got, skipped, err := parse(t, "kong", line+"\n"+line, ParseOptions{CallerField: "consumer"})
	if len(got) != 0 || skipped != 2 || err == nil {
		t.Errorf("--caller-field consumer: records = %v, skipped = %d, err = %v; want every line skipped", got, skipped, err)
	}
}

func TestKong_never_copies_headers_or_querystring(t *testing.T) {
	const secret = "SECRET-MARKER"
	in := `{"consumer":{"id":"c-id","username":"checkout-web"},"client_ip":"192.0.2.7","authenticated_entity":{"id":"` + marker + `"},` +
		`"request":{"headers":{"host":"gateway.example","apikey":"` + marker + `","cookie":"session=` + marker + `","x-client-id":"mobile-app"},` +
		`"uri":"/orders/1?token=` + marker + `","method":"GET","querystring":{"token":"` + marker + `"},` +
		`"url":"https://gateway.example/orders/1?token=` + marker + `"},"started_at":1794993164000}` + "\n" +
		// No host header: the host comes from request.url, whose query string carries the secret too.
		`{"request":{"headers":{"apikey":"` + marker + `"},"uri":"/orders/2?token=` + marker + `","method":"GET","querystring":{"token":"` + marker + `"},` +
		`"url":"https://gateway.example/orders/2?token=` + marker + `"},"started_at":1794993164000}` + "\n" +
		// A broken line holding the secret is counted, never echoed.
		`{"request":{"headers":{"apikey":"` + marker + `"},"uri":"/orders/3"` + "\n"
	for _, field := range []string{"", "request.headers.x-client-id", "consumer.custom_id"} {
		got, skipped, err := parse(t, "kong", in, ParseOptions{CallerField: field})
		if err != nil || len(got) != 2 || skipped != 1 {
			t.Fatalf("--caller-field %q: records = %v, skipped = %d, err = %v; want 2 records, 1 skipped", field, got, skipped, err)
		}
		for _, r := range got {
			if s := fmt.Sprintf("%+v", r); strings.Contains(s, secret) || strings.Contains(s, "token") || strings.Contains(s, "192.0.2.7") {
				t.Errorf("--caller-field %q: record carries input it must not: %s", field, s)
			}
		}
	}

	// Input with nothing readable: the error names no content.
	broken := `{"request":{"headers":{"apikey":"` + marker + `"},"uri":"/orders/3?token=` + marker + `"` + "\n"
	if _, _, err := parse(t, "kong", broken, ParseOptions{}); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("err = %v; want an error without the input", err)
	}
}
