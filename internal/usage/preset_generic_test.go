package usage

import (
	"strings"
	"testing"
)

// genericWant is what testdata/generic.csv and testdata/generic.json both hold.
var genericWant = []Record{
	{Caller: "checkout-web", Kind: "client", Method: "GET", Path: "/v1/orders/8812", Service: "orders", Count: 42, Day: "2026-11-17"},
	{Caller: "billing-worker", Kind: "service", Method: "POST", Path: "/v1/orders", Service: "orders", Count: 1, Day: "2026-11-18"},
	{Caller: "mobile app, ios", Kind: "client", Method: "GET", Path: "/orders/{}", Count: 3, Day: "2026-11-16"},
	{Caller: "", Kind: "client", Method: "GET", Path: "/v1/orders/77", Service: "orders", Count: 5, Day: "2026-11-15"},
}

func TestGeneric_csv(t *testing.T) {
	assertRecords(t, parseFixture(t, "generic", "generic.csv", ParseOptions{}), genericWant)

	// Column order is free, header names ignore case, unknown columns are ignored, and the optional columns
	// (callerKind, service, count) may be left out.
	in := "\xef\xbb\xbfLastSeen , Path,team,METHOD,caller\r\n2026-11-18,/v1/orders/9?x=1,ignored,delete, checkout-web \r\n"
	got, skipped, err := parse(t, "generic", in, ParseOptions{})
	if err != nil || skipped != 0 {
		t.Fatalf("skipped = %d, err = %v", skipped, err)
	}
	assertRecords(t, got, []Record{{Caller: "checkout-web", Method: "DELETE", Path: "/v1/orders/9", Count: 1, Day: "2026-11-18"}})

	// --caller-field names another column.
	in = "app,caller,method,path,lastSeen\ncheckout-web,someone,GET,/v1/orders/1,2026-11-18\n"
	got, _, err = parse(t, "generic", in, ParseOptions{CallerField: "app"})
	if err != nil || len(got) != 1 || got[0].Caller != "checkout-web" {
		t.Errorf("caller field: records = %v, err = %v", got, err)
	}

	// A normalized template in the path column matches itself.
	m := mustMatcher(t, shop(), "")
	if e, _, ok := m.Match(Record{Method: "GET", Path: "/orders/{}", Service: "orders"}); !ok || e.ID != "e1" {
		t.Errorf("template path: matched %v, %v; want e1", e.ID, ok)
	}
}

func TestGeneric_json_lines_and_array(t *testing.T) {
	assertRecords(t, parseFixture(t, "generic", "generic.json", ParseOptions{}), genericWant)

	lines := `{"caller":"checkout-web","callerKind":"client","service":"orders","method":"GET","path":"/v1/orders/8812?token=abc","count":42,"lastSeen":"2026-11-17"}
{"caller":"billing-worker","callerKind":"Service","service":"orders","method":"post","path":"/v1/orders","lastSeen":"2026-11-18T09:12:44.1234567Z"}

{"caller":"mobile app, ios","callerKind":"client","method":"GET","path":"/orders/{}","count":"3","lastSeen":"2026-11-16","extra":{"nested":[1,2]}}
{"callerKind":"client","service":"orders","method":"GET","path":"/v1/orders/77","count":5,"lastSeen":"2026-11-15"}
`
	got, skipped, err := parse(t, "generic", lines, ParseOptions{})
	if err != nil || skipped != 0 {
		t.Fatalf("skipped = %d, err = %v", skipped, err)
	}
	assertRecords(t, got, genericWant)
}

func TestGeneric_missing_header_is_error(t *testing.T) {
	for name, in := range map[string]string{
		"data first":     "checkout-web,client,orders,GET,/v1/orders/1,1,2026-11-18\n",
		"no path column": "caller,callerKind,service,method,count,lastSeen\ncheckout-web,client,orders,GET,1,2026-11-18\n",
		"caller field":   "caller,method,path,lastSeen\ncheckout-web,GET,/v1/orders/1,2026-11-18\n",
	} {
		opts := ParseOptions{}
		if name == "caller field" {
			opts.CallerField = "app"
		}
		got, _, err := parse(t, "generic", in, opts)
		if err == nil || len(got) != 0 {
			t.Errorf("%s: records = %v, err = %v; want an error", name, got, err)
			continue
		}
		if strings.Contains(err.Error(), "checkout-web") || strings.Contains(err.Error(), "/v1/orders") {
			t.Errorf("%s: error echoes the input: %v", name, err)
		}
	}
}
