package usage

import (
	"strings"
	"testing"
)

func TestNginx_json_log_format(t *testing.T) {
	assertRecords(t, parseFixture(t, "nginx", "nginx-usage.log", ParseOptions{}), []Record{
		{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "gateway.example", Path: "/orders/v1/orders/8812", Count: 1, Day: "2026-11-18"},
		{Caller: "billing-worker", Kind: "client", Method: "POST", Host: "gateway.example", Path: "/billing/invoices/41/lines/3", Count: 1, Day: "2026-11-17"},
		{Caller: "", Kind: "client", Method: "DELETE", Host: "", Path: "/orders/v1/orders/9", Count: 1, Day: "2026-11-16"},
		{Caller: "", Kind: "client", Method: "GET", Host: "gateway.example", Path: "/inventory/items", Count: 1, Day: "2026-11-16"},
	})

	// --caller-field names another member of the line; "-" there is anonymous too.
	in := `{"time":"2026-11-18T09:12:44+00:00","method":"GET","host":"gateway.example","path":"/orders/1","caller":"edge","client":"checkout-web"}` + "\n" +
		`{"time":"2026-11-18T09:12:44+00:00","method":"GET","host":"gateway.example","path":"/orders/1","caller":"edge","client":"-"}`
	got, skipped, err := parse(t, "nginx", in, ParseOptions{CallerField: "client"})
	if err != nil || skipped != 0 || len(got) != 2 || got[0].Caller != "checkout-web" || got[1].Caller != "" {
		t.Errorf("--caller-field client: records = %v, skipped = %d, err = %v", got, skipped, err)
	}
}

func TestNginx_time_with_offset_becomes_utc_day(t *testing.T) {
	in := `{"time":"2026-11-18T23:30:00-05:00","method":"GET","host":"gateway.example","path":"/orders/1","caller":"checkout-web"}`
	got, skipped, err := parse(t, "nginx", in, ParseOptions{})
	if err != nil || skipped != 0 || len(got) != 1 || got[0].Day != "2026-11-19" {
		t.Errorf("records = %v, skipped = %d, err = %v; want day 2026-11-19", got, skipped, err)
	}
}

func TestNginx_dash_and_empty_caller_are_anonymous(t *testing.T) {
	for _, caller := range []string{`""`, `"-"`, `" - "`, `null`} {
		in := `{"time":"2026-11-18T09:12:44+00:00","method":"GET","host":"gateway.example","path":"/orders/1","caller":` + caller + `}`
		got, skipped, err := parse(t, "nginx", in, ParseOptions{})
		if err != nil || skipped != 0 || len(got) != 1 || got[0].Caller != "" {
			t.Errorf("caller %s: records = %v, skipped = %d, err = %v; want one anonymous record", caller, got, skipped, err)
		}
	}
}

func TestNginx_skips_combined_format_lines(t *testing.T) {
	jsonLine := `{"time":"2026-11-18T09:12:44+00:00","method":"GET","host":"gateway.example","path":"/orders/1?token=abc","caller":"checkout-web"}`
	combined := `203.0.113.9 - alice [18/Nov/2026:09:12:44 +0000] "GET /orders/1?token=` + marker + ` HTTP/1.1" 200 612 "-" "curl/8.4.0"`
	in := combined + "\n" + jsonLine + "\n" + combined + "\n" + `["not","an","object"]` + "\n" + jsonLine + "\n"
	got, skipped, err := parse(t, "nginx", in, ParseOptions{})
	if err != nil || skipped != 3 {
		t.Fatalf("records = %v, skipped = %d, err = %v; want 3 skipped", got, skipped, err)
	}
	want := Record{Caller: "checkout-web", Kind: "client", Method: "GET", Host: "gateway.example", Path: "/orders/1", Count: 1, Day: "2026-11-18"}
	assertRecords(t, got, []Record{want, want})

	// A file of combined lines only has nothing to read: an error that does not echo the lines.
	_, _, err = parse(t, "nginx", combined+"\n"+combined+"\n", ParseOptions{})
	if err == nil || strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "alice") {
		t.Errorf("err = %v; want an error without the input", err)
	}
}
