package usage

import (
	"errors"
	"fmt"
	"math"
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
	if !errors.As(err, &tooMany) || tooMany.Limit != 2 || err.Error() != "too many distinct callers (more than 2, limit 2)" {
		t.Errorf("err = %#v (%v), want Limit 2, \"more than 2\"", err, err)
	}

}

// Distinct callers are counted as records arrive: once the limit is passed no row and no caller is kept any more, so
// memory stays bounded whatever the input holds, and the snapshot is refused.
func TestAggregate_caller_limit_bounds_memory(t *testing.T) {
	tpl := shop()
	tpl.Limits = Limits{MaxCallers: 10}
	a := NewAggregator(mustMatcher(t, tpl, ""), false)
	for i := 0; i < 200000; i++ {
		a.Add(Record{Caller: fmt.Sprintf("caller-%d", i), Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-12"})
		if len(a.rows) > 10 || len(a.callers) > 10 {
			t.Fatalf("after %d callers: %d rows, %d callers kept, want at most 10", i+1, len(a.rows), len(a.callers))
		}
	}
	// A caller already counted still adds to its rows.
	a.Add(Record{Caller: "caller-3", Kind: "client", Method: "POST", Host: "orders.internal", Path: "/v1/orders", Count: 1, Day: "2026-11-12"})
	if len(a.rows) != 11 {
		t.Fatalf("%d rows, want 11 (a known caller's new endpoint)", len(a.rows))
	}
	_, err := a.Snapshot("apim", "1.4.0")
	var tooMany *TooManyCallersError
	if !errors.As(err, &tooMany) || tooMany.Limit != 10 || err.Error() != "too many distinct callers (more than 10, limit 10)" {
		t.Fatalf("err = %v, want too many distinct callers (more than 10, limit 10)", err)
	}
}

// Rows stop growing at the row limit; the snapshot is then too large, as Encode would say.
func TestAggregate_row_limit_bounds_memory(t *testing.T) {
	tpl := shop()
	tpl.Limits = Limits{MaxCallers: 1000, MaxRows: 5}
	a := NewAggregator(mustMatcher(t, tpl, ""), false)
	for i := 0; i < 500; i++ {
		for _, path := range []string{"/v1/orders/1", "/v1/orders"} {
			a.Add(Record{Caller: fmt.Sprintf("caller-%d", i), Kind: "client", Method: "GET", Host: "orders.internal", Path: path, Count: 1, Day: "2026-11-12"})
		}
		if len(a.rows) > 5 {
			t.Fatalf("%d rows, want at most 5", len(a.rows))
		}
	}
	if _, err := a.Snapshot("apim", "1.4.0"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

// The local table of unmatched requests keeps at most maxUnmatchedKeys host and prefix pairs; the requests of any
// further pair are counted in one "(other)" entry. Nothing is lost from the totals.
func TestAggregate_unmatched_table_is_capped(t *testing.T) {
	a := NewAggregator(mustMatcher(t, shop(), ""), false)
	const hosts = maxUnmatchedKeys + 3000
	for i := 0; i < hosts; i++ {
		a.Add(Record{Caller: "checkout-web", Kind: "client", Method: "GET", Host: fmt.Sprintf("host-%d.example", i), Path: "/nope/x", Count: 1, Day: "2026-11-12"})
		if len(a.unmatched) > maxUnmatchedKeys {
			t.Fatalf("%d unmatched keys, want at most %d", len(a.unmatched), maxUnmatchedKeys)
		}
	}
	// A pair already in the table still counts there.
	a.Add(Record{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "host-0.example", Path: "/nope/x", Count: 5, Day: "2026-11-12"})
	list := a.UnmatchedByPrefix()
	if len(list) != maxUnmatchedKeys+1 {
		t.Fatalf("%d entries, want %d", len(list), maxUnmatchedKeys+1)
	}
	var total, other int64
	for _, p := range list {
		total += p.Count
		if p.Other {
			other += p.Count
		}
	}
	// Most requests first: the "(other)" entry (3000) leads, then host-0 (6).
	if total != hosts+5 || other != 3000 || !list[0].Other || list[1].Host != "host-0.example" || list[1].Count != 6 {
		t.Fatalf("total %d, other %d, first two %+v %+v; want %d, 3000, (other) then host-0", total, other, list[0], list[1], hosts+5)
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
	if !errors.As(err, &tooMany) || tooMany.Limit != 2000 {
		t.Errorf("2001 callers with the default limit: err = %v, want Limit 2000", err)
	}
}

// Counts saturate at the largest int64: two huge pre-aggregated rows never wrap to a negative count.
func TestAggregate_counts_saturate(t *testing.T) {
	a := NewAggregator(mustMatcher(t, shop(), ""), false)
	for i := 0; i < 2; i++ {
		for _, r := range []Record{
			{Caller: "checkout-web", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1"},
			{Caller: "", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1"},
			{Caller: "jane@contoso.com", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1"},
			{Caller: "checkout-web", Method: "GET", Host: "orders.internal", Path: "/nope/x"},
		} {
			r.Count, r.Day = math.MaxInt64, "2026-11-18"
			a.Add(r)
		}
	}
	s, err := a.Snapshot("generic", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 1 || countOf(s.Rows[0]) != math.MaxInt64 || s.AnonymousRequests != math.MaxInt64 ||
		s.WithheldRequests != math.MaxInt64 || s.UnmatchedRequests != math.MaxInt64 {
		t.Fatalf("snapshot %+v (row count %d)", s, countOf(s.Rows[0]))
	}
	if u := a.UnmatchedByPrefix(); len(u) != 1 || u[0].Count != math.MaxInt64 {
		t.Fatalf("unmatched %+v", u)
	}
	body, err := Encode(s)
	if err != nil || !strings.Contains(string(body), `"count":9223372036854775807`) {
		t.Fatalf("body %s, %v", body, err)
	}
	if AddCount(math.MaxInt64-1, 1) != math.MaxInt64 || AddCount(2, 3) != 5 || AddCount(0, 0) != 0 {
		t.Fatal("AddCount")
	}
}

// The unmatched table keeps the first two path segments; a segment that may be an id, an address, an encoded value or
// a token (a digit, "@", "%", or more than 24 characters) is kept as "*", so neither reaches the job log and ids do
// not make the table grow.
func TestPathPrefix_masks_segments(t *testing.T) {
	for path, want := range map[string]string{
		"/legacy/orders/1":        "/legacy/orders",
		"/legacy/8812":            "/legacy/*",
		"/v1/orders/8812":         "/*/orders",
		"/users/jane@contoso.com": "/users/*",
		"/files/a%2Fb":            "/files/*",
		"/reset-password/eyJhbGciOiJIUzI1NiJ9.SECRETTOKEN": "/reset-password/*",
		"/abcdefghijklmnopqrstuvwx/y":                      "/abcdefghijklmnopqrstuvwx/y", // 24 characters
		"/abcdefghijklmnopqrstuvwxy/y":                     "/*/y",                        // 25
		"/orders/{}":                                       "/orders/{}",
		"/éééééééééééééééééééééééé":                        "/éééééééééééééééééééééééé", // 24 characters, 48 bytes
		"/": "/",
		"":  "/",
	} {
		if got := pathPrefix(path); got != want {
			t.Errorf("pathPrefix(%q) = %q, want %q", path, got, want)
		}
	}

	// Ids collapse into one entry.
	a := NewAggregator(mustMatcher(t, shop(), ""), false)
	for i := 0; i < 5000; i++ {
		a.Add(Record{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "orders.internal", Path: fmt.Sprintf("/users/%d", i), Count: 1, Day: "2026-11-12"})
	}
	if list := a.UnmatchedByPrefix(); len(list) != 1 || list[0].Prefix != "/users/*" || list[0].Count != 5000 {
		t.Fatalf("list = %+v", list)
	}
}

// The guard runs again on the name as it is cut to 200 characters: a cut can turn "10.0.0.1299" into the IP address
// "10.0.0.1", and such a name is withheld, never sent.
func TestAggregate_name_withheld_after_the_cut(t *testing.T) {
	name := strings.Repeat("team-zz ", 24) + "10.0.0.1299" // 203 characters; not withheld whole
	if LooksPersonal(name) || !LooksPersonal(name[:maxCallerRunes]) {
		t.Fatal("the example no longer shows a name that only the cut makes personal")
	}
	a := NewAggregator(mustMatcher(t, shop(), ""), false)
	a.Add(Record{Caller: name, Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 4, Day: "2026-11-12"})
	s, err := a.Snapshot("apim", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 0 || s.WithheldRequests != 4 {
		t.Fatalf("rows %+v, withheld %d; want no row, 4 withheld", s.Rows, s.WithheldRequests)
	}
}
