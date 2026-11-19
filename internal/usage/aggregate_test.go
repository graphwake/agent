package usage

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func countOf(r Row) int64 {
	if r.Count == nil {
		return -1
	}
	return *r.Count
}

func TestAggregate_counters_and_rows(t *testing.T) {
	m := mustMatcher(t, shop(), "")
	a := NewAggregator(m, false)
	long := strings.Repeat("é", 250)
	for _, r := range []Record{
		{Caller: "  ", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 3, Day: "2026-11-01"},
		{Caller: "jane@contoso.com", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 2, Day: "2026-11-15"},
		{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/nope/x/y", Count: 4, Day: "2026-11-05"},
		{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/nope/x/z", Count: 1, Day: "2026-11-05"},
		{Caller: "checkout-web", Kind: "client", Method: "GET", Path: "/other", Count: 2, Day: "2026-11-05"},
		{Caller: " checkout-web ", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/8812", Count: 5, Day: "2026-11-12"},
		{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/7", Count: 1, Day: "2026-11-10"},
		{Caller: "checkout-web", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/7", Count: 1, Day: "2026-11-10"},
		{Caller: "billing-worker", Kind: "service", Method: "GET", Host: "billing.internal", Path: "/invoices/3", Count: 9, Day: "2026-11-11"},
		{Caller: "billing-worker", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/export", Count: 2, Day: "2026-11-09"},
		{Caller: long, Kind: "client", Method: "POST", Host: "orders.internal", Path: "/v1/orders", Count: 1, Day: "2026-11-03"},
	} {
		a.Add(r)
	}

	s, err := a.Snapshot("apim", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if s.Preset != "apim" || s.AgentVersion != "1.4.0" || !s.CountsIncluded {
		t.Errorf("header = %q %q %v", s.Preset, s.AgentVersion, s.CountsIncluded)
	}
	if s.WindowStart != "2026-11-01" || s.WindowEnd != "2026-11-15" {
		t.Errorf("window = %s..%s, want 2026-11-01..2026-11-15", s.WindowStart, s.WindowEnd)
	}
	if s.AnonymousRequests != 3 || s.WithheldRequests != 2 || s.UnmatchedRequests != 7 {
		t.Errorf("anonymous/withheld/unmatched = %d/%d/%d, want 3/2/7", s.AnonymousRequests, s.WithheldRequests, s.UnmatchedRequests)
	}

	type got struct {
		Caller, Kind, EndpointID, Service, Method, Path string
		Count                                           int64
		LastSeen                                        string
	}
	var rows []got
	for _, r := range s.Rows {
		rows = append(rows, got{r.Caller, r.CallerKind, r.EndpointID, r.Service, r.Method, r.Path, countOf(r), r.LastSeen})
	}
	want := []got{
		{"billing-worker", "service", "e10", "billing", "GET", "/invoices/{}", 9, "2026-11-11"},
		{"billing-worker", "service", "e2", "orders", "GET", "/orders/export", 2, "2026-11-09"},
		{"checkout-web", "client", "e1", "orders", "GET", "/orders/{}", 6, "2026-11-12"},
		{"checkout-web", "service", "e1", "orders", "GET", "/orders/{}", 1, "2026-11-10"},
		{strings.Repeat("é", 200), "client", "e3", "orders", "POST", "/orders", 1, "2026-11-03"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows =\n%+v\nwant\n%+v", rows, want)
	}

	prefixes := a.UnmatchedByPrefix()
	wantPrefixes := []PrefixCount{
		{Host: "orders.internal", Prefix: "/nope/x", Count: 5},
		{Host: "", Prefix: "/other", Count: 2},
	}
	if !reflect.DeepEqual(prefixes, wantPrefixes) {
		t.Errorf("UnmatchedByPrefix = %+v, want %+v", prefixes, wantPrefixes)
	}

	body, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "8812") || strings.Contains(string(body), "jane") {
		t.Errorf("encoded body leaks a raw path or a withheld name: %s", body)
	}
}

func TestAggregate_empty_snapshot_sends_empty_rows(t *testing.T) {
	a := NewAggregator(mustMatcher(t, shop(), ""), false)
	s, err := a.Snapshot("generic", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	body, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"rows":[]`) {
		t.Errorf("empty snapshot body = %s, want rows as []", body)
	}
}

func TestAggregate_no_counts(t *testing.T) {
	a := NewAggregator(mustMatcher(t, shop(), ""), true)
	a.Add(Record{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 5, Day: "2026-11-12"})
	a.Add(Record{Caller: "", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 2, Day: "2026-11-12"})
	s, err := a.Snapshot("otel", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if s.CountsIncluded {
		t.Error("CountsIncluded = true with noCounts")
	}
	if len(s.Rows) != 1 || s.Rows[0].Count != nil {
		t.Fatalf("rows = %+v, want one row without a count", s.Rows)
	}
	if s.AnonymousRequests != 2 {
		t.Errorf("AnonymousRequests = %d, want 2 (the counters stay)", s.AnonymousRequests)
	}
	body, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"count"`) {
		t.Errorf("body has a count member with noCounts: %s", body)
	}
}

func TestAggregate_too_many_callers(t *testing.T) {
	tpl := shop()
	tpl.Limits = Limits{MaxCallers: 2}
	add := func(a *Aggregator, callers ...string) {
		for _, c := range callers {
			a.Add(Record{Caller: c, Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-12"})
		}
	}

	a := NewAggregator(mustMatcher(t, tpl, ""), false)
	add(a, "a", "b", "a")
	if _, err := a.Snapshot("apim", "1.4.0"); err != nil {
		t.Fatalf("two callers with limit 2: %v", err)
	}

	add(a, "c")
	_, err := a.Snapshot("apim", "1.4.0")
	if !errors.Is(err, ErrTooManyCallers) {
		t.Fatalf("three callers with limit 2: err = %v, want ErrTooManyCallers", err)
	}
	var tooMany *TooManyCallersError
	if !errors.As(err, &tooMany) || tooMany.Callers != 3 || tooMany.Limit != 2 {
		t.Errorf("err = %#v, want Callers 3, Limit 2", err)
	}

}

func TestAggregate_default_caller_limit(t *testing.T) {
	// No limit in the templates (0 or missing): the agent's default of 2000 applies.
	a := NewAggregator(mustMatcher(t, shop(), ""), false)
	for i := 0; i < DefaultMaxCallers; i++ {
		a.Add(Record{Caller: fmt.Sprintf("caller-%d", i), Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-12"})
	}
	if _, err := a.Snapshot("apim", "1.4.0"); err != nil {
		t.Fatalf("2000 callers with the default limit: %v", err)
	}
	a.Add(Record{Caller: "one-too-many", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-12"})
	_, err := a.Snapshot("apim", "1.4.0")
	var tooMany *TooManyCallersError
	if !errors.As(err, &tooMany) || tooMany.Callers != 2001 || tooMany.Limit != 2000 {
		t.Errorf("2001 callers with the default limit: err = %v, want Callers 2001, Limit 2000", err)
	}
}
