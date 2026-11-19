package usage

import (
	"strings"
	"testing"
)

func TestOtel_client_spans_only(t *testing.T) {
	assertRecords(t, parseFixture(t, "otel", "otel-file-exporter.jsonl", ParseOptions{}), []Record{
		// New semantic conventions (http.request.method, url.full); the server span and the database client span
		// on the same line are ignored.
		{Caller: "checkout-web", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/8812", Count: 1, Day: "2026-11-18"},
		// Old conventions (http.method, http.url).
		{Caller: "checkout-web", Kind: "service", Method: "POST", Host: "billing.internal", Path: "/invoices/41/lines/3", Count: 1, Day: "2026-11-17"},
		// kind "SPAN_KIND_CLIENT", numeric nanos, no URL: server.address and url.path.
		{Caller: "billing-worker", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/77", Count: 1, Day: "2026-11-16"},
		// No service.name: anonymous. net.peer.name and http.target. The span after it has no path and is dropped.
		{Caller: "", Kind: "service", Method: "DELETE", Host: "orders.internal", Path: "/v1/orders/9", Count: 1, Day: "2026-11-16"},
	})
}

func TestOtel_drops_query_string(t *testing.T) {
	for _, r := range parseFixture(t, "otel", "otel-file-exporter.jsonl", ParseOptions{}) {
		if strings.ContainsAny(r.Path, "?#") || strings.Contains(r.Path, "token") {
			t.Errorf("path %q keeps the query string", r.Path)
		}
		if strings.Contains(r.Host, ":") {
			t.Errorf("host %q keeps the port", r.Host)
		}
	}
}

func TestOtel_caller_field_override(t *testing.T) {
	line := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout"}},{"key":"service.namespace","value":{"stringValue":"shop"}}]},"scopeSpans":[{"spans":[{"kind":3,"startTimeUnixNano":"1794993164000000000","attributes":[{"key":"http.request.method","value":{"stringValue":"GET"}},{"key":"url.full","value":{"stringValue":"https://orders.internal/v1/orders/1"}}]}]}]}]}`
	got, _, err := parse(t, "otel", line, ParseOptions{CallerField: "service.namespace"})
	if err != nil || len(got) != 1 || got[0].Caller != "shop" {
		t.Errorf("records = %v, err = %v; want caller shop", got, err)
	}
}
