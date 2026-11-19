package usage

import (
	"strings"
	"testing"
)

// appInsightsRecords is what both fixtures hold.
var appInsightsRecords = []Record{
	{Caller: "checkout-web", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/8812", Count: 42, Day: "2026-11-18"},
	{Caller: "billing-worker", Kind: "service", Method: "POST", Host: "orders.internal", Path: "/v1/orders", Count: 7, Day: "2026-11-17"},
	{Caller: "", Kind: "service", Method: "GET", Host: "inventory.internal", Path: "/items", Count: 3, Day: "2026-11-16"},
}

// aiTable is an `az monitor app-insights query` answer with the query's columns and the given rows.
func aiTable(rows ...string) string {
	return `{"tables":[{"name":"PrimaryResult","columns":[{"name":"AppRoleName","type":"string"},{"name":"Host","type":"string"},` +
		`{"name":"Method","type":"string"},{"name":"Path","type":"string"},{"name":"Calls","type":"long"},{"name":"LastSeen","type":"datetime"}],` +
		`"rows":[` + strings.Join(rows, ",") + `]}]}`
}

func TestAppInsights_tables_shape(t *testing.T) {
	assertRecords(t, parseFixture(t, "appinsights", "appinsights-tables.json", ParseOptions{}), appInsightsRecords)

	cases := map[string]string{
		// Classic Application Insights names the caller column cloud_RoleName; column names are matched ignoring case.
		"classic cloud_RoleName": `{"tables":[{"name":"PrimaryResult","columns":[{"name":"cloud_RoleName"},{"name":"host"},{"name":"METHOD"},` +
			`{"name":"Path"},{"name":"Calls"},{"name":"LastSeen"}],"rows":[["checkout-web","orders.internal","GET","/v1/orders/8812",42,"2026-11-18T09:12:44Z"]]}]}`,
		// Rows before columns (the API does not promise an order).
		"rows before columns": `{"tables":[{"rows":[["checkout-web","orders.internal","GET","/v1/orders/8812",42,"2026-11-18T09:12:44Z"]],` +
			`"name":"PrimaryResult","columns":[{"name":"AppRoleName"},{"name":"Host"},{"name":"Method"},{"name":"Path"},{"name":"Calls"},{"name":"LastSeen"}]}]}`,
		// Other members of the answer are skipped unread.
		"other members": `{"statistics":{"query":{"executionTime":0.1}},"tables":[{"name":"PrimaryResult","columns":[{"name":"AppRoleName"},{"name":"Host"},` +
			`{"name":"Method"},{"name":"Path"},{"name":"Calls"},{"name":"LastSeen"}],"rows":[["checkout-web","orders.internal","GET","/v1/orders/8812",42,"2026-11-18T09:12:44Z"]]}],"render":null}`,
	}
	want := []Record{{Caller: "checkout-web", Kind: "service", Method: "GET", Host: "orders.internal", Path: "/v1/orders/8812", Count: 42, Day: "2026-11-18"}}
	for name, in := range cases {
		got, skipped, err := parse(t, "appinsights", in, ParseOptions{})
		if err != nil || skipped != 0 {
			t.Fatalf("%s: skipped = %d, err = %v", name, skipped, err)
		}
		assertRecords(t, got, want)
	}

	// --caller-field picks another column.
	in := `{"tables":[{"columns":[{"name":"AppRoleName"},{"name":"Team"},{"name":"Method"},{"name":"Path"},{"name":"Calls"},{"name":"LastSeen"}],` +
		`"rows":[["checkout-web","payments","GET","/x",1,"2026-11-18T09:12:44Z"]]}]}`
	got, skipped, err := parse(t, "appinsights", in, ParseOptions{CallerField: "Team"})
	if err != nil || skipped != 0 || len(got) != 1 || got[0].Caller != "payments" {
		t.Fatalf("caller field: records = %v, skipped = %d, err = %v", got, skipped, err)
	}
}

func TestAppInsights_log_analytics_shape_gives_the_same_records(t *testing.T) {
	tables := parseFixture(t, "appinsights", "appinsights-tables.json", ParseOptions{})
	logAnalytics := parseFixture(t, "appinsights", "appinsights-log-analytics.json", ParseOptions{})
	assertRecords(t, logAnalytics, tables)
	assertRecords(t, logAnalytics, appInsightsRecords)

	// One row object per line reads the same.
	lines := `{"AppRoleName":"checkout-web","Calls":"42","Host":"orders.internal","LastSeen":"2026-11-18T09:12:44.123Z","Method":"GET","Path":"/v1/orders/8812","TableName":"PrimaryResult"}` + "\n" +
		`{"AppRoleName":"billing-worker","Calls":"7","Host":"Orders.Internal:443","LastSeen":"2026-11-17T23:59:59.9999999Z","Method":"post","Path":"/v1/orders","TableName":"PrimaryResult"}` + "\n"
	got, skipped, err := parse(t, "appinsights", lines, ParseOptions{})
	if err != nil || skipped != 0 {
		t.Fatalf("lines: skipped = %d, err = %v", skipped, err)
	}
	assertRecords(t, got, appInsightsRecords[:2])
}

func TestAppInsights_none_and_empty_role_are_anonymous(t *testing.T) {
	inputs := map[string]string{
		"tables": aiTable(
			`[null,"orders.internal","GET","/x",1,"2026-11-18T09:12:44Z"]`,
			`["","orders.internal","GET","/x",1,"2026-11-18T09:12:44Z"]`,
			`["None","orders.internal","GET","/x",1,"2026-11-18T09:12:44Z"]`,
			`["  ","orders.internal","GET","/x",1,"2026-11-18T09:12:44Z"]`,
		),
		"log analytics": `[{"AppRoleName":"None","Host":"orders.internal","Method":"GET","Path":"/x","Calls":"1","LastSeen":"2026-11-18T09:12:44Z"},` +
			`{"AppRoleName":"","Host":"orders.internal","Method":"GET","Path":"/x","Calls":"1","LastSeen":"2026-11-18T09:12:44Z"},` +
			`{"Host":"orders.internal","Method":"GET","Path":"/x","Calls":"1","LastSeen":"2026-11-18T09:12:44Z"},` +
			`{"AppRoleName":null,"Host":"orders.internal","Method":"GET","Path":"/x","Calls":"1","LastSeen":"2026-11-18T09:12:44Z"}]`,
	}
	for name, in := range inputs {
		got, skipped, err := parse(t, "appinsights", in, ParseOptions{})
		if err != nil || skipped != 0 || len(got) != 4 {
			t.Fatalf("%s: records = %v, skipped = %d, err = %v", name, got, skipped, err)
		}
		for i, r := range got {
			if r.Caller != "" {
				t.Errorf("%s row %d: caller = %q, want anonymous", name, i, r.Caller)
			}
		}
	}
}

// The query's Method is empty when the dependency name does not start with an HTTP method: such a row is skipped and
// counted, whether the method is "", null or "None".
func TestAppInsights_row_without_method_is_skipped(t *testing.T) {
	inputs := map[string]string{
		"tables": aiTable(
			`["checkout-web","orders.internal","GET","/x",1,"2026-11-18T09:12:44Z"]`,
			`["checkout-web","orders.internal","","/x",1,"2026-11-18T09:12:44Z"]`,
			`["checkout-web","orders.internal",null,"/x",1,"2026-11-18T09:12:44Z"]`,
			`["checkout-web","orders.internal","None","/x",1,"2026-11-18T09:12:44Z"]`,
		),
		"log analytics": `[{"AppRoleName":"checkout-web","Host":"orders.internal","Method":"GET","Path":"/x","Calls":"1","LastSeen":"2026-11-18T09:12:44Z"},` +
			`{"AppRoleName":"checkout-web","Host":"orders.internal","Method":"","Path":"/x","Calls":"1","LastSeen":"2026-11-18T09:12:44Z"},` +
			`{"AppRoleName":"checkout-web","Host":"orders.internal","Path":"/x","Calls":"1","LastSeen":"2026-11-18T09:12:44Z"},` +
			`{"AppRoleName":"checkout-web","Host":"orders.internal","Method":"None","Path":"/x","Calls":"1","LastSeen":"2026-11-18T09:12:44Z"}]`,
	}
	for name, in := range inputs {
		got, skipped, err := parse(t, "appinsights", in, ParseOptions{})
		if err != nil || skipped != 3 || len(got) != 1 || got[0].Method != "GET" {
			t.Errorf("%s: records = %v, skipped = %d, err = %v; want 1 record, 3 skipped", name, got, skipped, err)
		}
	}
}

func TestAppInsights_calls_as_number_and_as_string(t *testing.T) {
	inputs := map[string]string{
		"number":           aiTable(`["checkout-web","orders.internal","GET","/x",12,"2026-11-18T09:12:44Z"]`),
		"string in tables": aiTable(`["checkout-web","orders.internal","GET","/x","12","2026-11-18T09:12:44Z"]`),
		"log analytics":    `[{"AppRoleName":"checkout-web","Host":"orders.internal","Method":"GET","Path":"/x","Calls":"12","LastSeen":"2026-11-18T09:12:44Z"}]`,
	}
	for name, in := range inputs {
		got, skipped, err := parse(t, "appinsights", in, ParseOptions{})
		if err != nil || skipped != 0 || len(got) != 1 || got[0].Count != 12 {
			t.Errorf("%s: records = %v, skipped = %d, err = %v; want one record with count 12", name, got, skipped, err)
		}
	}
	// A count that is not a whole number of at least one is skipped.
	got, skipped, err := parse(t, "appinsights", aiTable(
		`["checkout-web","orders.internal","GET","/x",12,"2026-11-18T09:12:44Z"]`,
		`["checkout-web","orders.internal","GET","/x",0,"2026-11-18T09:12:44Z"]`,
		`["checkout-web","orders.internal","GET","/x","many","2026-11-18T09:12:44Z"]`,
		`["checkout-web","orders.internal","GET","/x",true,"2026-11-18T09:12:44Z"]`,
	), ParseOptions{})
	if err != nil || skipped != 3 || len(got) != 1 {
		t.Errorf("bad counts: records = %v, skipped = %d, err = %v", got, skipped, err)
	}
}
