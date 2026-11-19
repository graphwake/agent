package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApim_log_analytics_output(t *testing.T) {
	assertRecords(t, parseFixture(t, "apim", "apim-log-analytics.json", ParseOptions{}), []Record{
		{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "gateway.example", Path: "/orders/v1/orders/8812", API: "orders-api", Count: 42, Day: "2026-11-18"},
		{Caller: "", Kind: "client", Method: "POST", Host: "gateway.example", Path: "/orders/v1/orders", API: "orders-api", Count: 7, Day: "2026-11-17"},
		{Caller: "partner-portal", Kind: "client", Method: "GET", Host: "", Path: "/inventory/items", API: "", Count: 1, Day: "2026-11-16"},
	})

	// A path that still carries a query string (a hand-written query without parse_url) loses it, and an empty
	// subscription id is anonymous like "None".
	in := `[{"ApimSubscriptionId":"","ApiId":"orders-api","Method":"GET","Host":"gateway.example","Path":"/orders/v1/orders/8812?token=abc#x","Calls":"3","LastSeen":"2026-11-18T09:12:44Z","TableName":"PrimaryResult"}]`
	got, skipped, err := parse(t, "apim", in, ParseOptions{})
	if err != nil || skipped != 0 {
		t.Fatalf("skipped = %d, err = %v", skipped, err)
	}
	assertRecords(t, got, []Record{{Kind: "client", Method: "GET", Host: "gateway.example", Path: "/orders/v1/orders/8812", API: "orders-api", Count: 3, Day: "2026-11-18"}})
}

func TestApim_caller_field_override(t *testing.T) {
	in := `[
  {"ApimSubscriptionId":"master","CallerApp":"checkout-web","ApiId":"orders-api","Method":"GET","Host":"gateway.example","Path":"/orders/1","Calls":"2","LastSeen":"2026-11-18T09:12:44Z","TableName":"PrimaryResult"},
  {"ApimSubscriptionId":"master","CallerApp":"None","ApiId":"orders-api","Method":"GET","Host":"gateway.example","Path":"/orders/2","Calls":"1","LastSeen":"2026-11-18T09:12:44Z","TableName":"PrimaryResult"},
  {"ApimSubscriptionId":"master","ApiId":"orders-api","Method":"GET","Host":"gateway.example","Path":"/orders/3","Calls":"1","LastSeen":"2026-11-18T09:12:44Z","TableName":"PrimaryResult"}
]`
	got, skipped, err := parse(t, "apim", in, ParseOptions{CallerField: "CallerApp"})
	if err != nil || skipped != 0 || len(got) != 3 {
		t.Fatalf("records = %v, skipped = %d, err = %v", got, skipped, err)
	}
	for i, want := range []string{"checkout-web", "", ""} {
		if got[i].Caller != want {
			t.Errorf("row %d caller = %q, want %q", i, got[i].Caller, want)
		}
	}
}

// Two query answers in one file (two arrays): the rows of both.
func TestApim_several_arrays(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "apim-log-analytics.json"))
	if err != nil {
		t.Fatal(err)
	}
	one := parseFixture(t, "apim", "apim-log-analytics.json", ParseOptions{})
	for name, in := range map[string]string{
		"one after another": strings.TrimSpace(string(data)) + strings.TrimSpace(string(data)),
		"on separate lines": string(data) + "\n" + string(data),
	} {
		got, skipped, err := parse(t, "apim", in, ParseOptions{})
		if err != nil || skipped != 0 {
			t.Fatalf("%s: skipped = %d, err = %v", name, skipped, err)
		}
		assertRecords(t, got, append(append([]Record{}, one...), one...))
	}
}
