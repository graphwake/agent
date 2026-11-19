package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/graphwake/agent/internal/ingest"
)

var (
	snapshotKeys = []string{"agentVersion", "anonymousRequests", "countsIncluded", "preset", "rows", "unmatchedRequests", "windowEnd", "windowStart", "withheldRequests"}
	rowKeys      = []string{"caller", "callerKind", "count", "endpointId", "lastSeen", "method", "path", "service"}
)

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonNames lists the JSON member names of a struct's exported fields.
func jsonNames(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// The snapshot is everything that leaves the machine. Adding a member must be a deliberate change to this test.
func TestSnapshot_field_list_is_closed(t *testing.T) {
	n := int64(3)
	s := Snapshot{
		Preset: "apim", AgentVersion: "1.4.0", WindowStart: "2026-11-01", WindowEnd: "2026-11-15", CountsIncluded: true,
		UnmatchedRequests: 1, WithheldRequests: 2, AnonymousRequests: 3,
		Rows: []Row{{Caller: "checkout-web", CallerKind: "client", EndpointID: "e1", Service: "orders", Method: "GET",
			Path: "/orders/{}", Count: &n, LastSeen: "2026-11-15"}},
	}
	body, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(top); !reflect.DeepEqual(got, snapshotKeys) {
		t.Errorf("snapshot members = %v, want %v", got, snapshotKeys)
	}
	rows, _ := top["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", top["rows"])
	}
	row, _ := rows[0].(map[string]any)
	if got := sortedKeys(row); !reflect.DeepEqual(got, rowKeys) {
		t.Errorf("row members = %v, want %v", got, rowKeys)
	}

	// Also catch a new field that a filled-in literal would hide (an omitempty member left empty).
	if got := jsonNames(reflect.TypeOf(Snapshot{})); !reflect.DeepEqual(got, snapshotKeys) {
		t.Errorf("Snapshot fields = %v, want %v", got, snapshotKeys)
	}
	if got := jsonNames(reflect.TypeOf(Row{})); !reflect.DeepEqual(got, rowKeys) {
		t.Errorf("Row fields = %v, want %v", got, rowKeys)
	}
}

func TestTemplates_decode(t *testing.T) {
	raw := `{"source":{"id":"src","name":"gateway","preset":"apim"},"limits":{"maxCallers":2000,"maxRows":50000},
		"mappings":[{"host":"api.contoso.com","pathPrefix":"/shop","serviceId":"s1","stripPrefix":"/shop/v1"},{"api":"orders-api","serviceId":"s1"}],
		"services":[{"id":"s1","name":"orders","serverUrls":["https://orders.internal/v1"],"endpoints":[{"id":"e1","method":"GET","path":"/orders/{}"}]}]}`
	var got Templates
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	want := Templates{
		Source: SourceInfo{ID: "src", Name: "gateway", Preset: "apim"},
		Limits: Limits{MaxCallers: 2000, MaxRows: 50000},
		Mappings: []Mapping{
			{Host: "api.contoso.com", PathPrefix: "/shop", ServiceID: "s1", StripPrefix: "/shop/v1"},
			{API: "orders-api", ServiceID: "s1"},
		},
		Services: []Service{{ID: "s1", Name: "orders", ServerURLs: []string{"https://orders.internal/v1"},
			Endpoints: []Endpoint{{ID: "e1", Method: "GET", Path: "/orders/{}"}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded =\n%+v\nwant\n%+v", got, want)
	}
}

func TestEncode_limits(t *testing.T) {
	tpl := shop()
	tpl.Limits = Limits{MaxRows: 1}
	a := NewAggregator(mustMatcher(t, tpl, ""), false)
	a.Add(Record{Caller: "a", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-12"})
	s, err := a.Snapshot("apim", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(s); err != nil {
		t.Fatalf("one row with MaxRows 1: %v", err)
	}
	a.Add(Record{Caller: "b", Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-12"})
	s, err = a.Snapshot("apim", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(s); !errors.Is(err, ErrTooLarge) {
		t.Errorf("two rows with MaxRows 1: err = %v, want ErrTooLarge", err)
	}

	big := Snapshot{Rows: []Row{{Caller: strings.Repeat("x", ingest.MaxBodyBytes)}}}
	if _, err := Encode(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("body over 10 MB: err = %v, want ErrTooLarge", err)
	}

	body, err := Encode(Snapshot{Preset: "generic", Rows: []Row{{Path: "/a/{}&<b>"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"/a/{}&<b>"`) || strings.HasSuffix(string(body), "\n") {
		t.Errorf("body = %q, want no HTML escaping and no trailing newline", body)
	}
}

func TestEncode_default_row_limit(t *testing.T) {
	// No row limit in the templates (0 or missing): the agent's default of 50000 applies.
	tpl := shop()
	tpl.Limits = Limits{MaxCallers: 100000}
	a := NewAggregator(mustMatcher(t, tpl, ""), false)
	add := func(caller string) {
		a.Add(Record{Caller: caller, Kind: "client", Method: "GET", Host: "orders.internal", Path: "/v1/orders/1", Count: 1, Day: "2026-11-12"})
	}
	for i := 0; i < DefaultMaxRows; i++ {
		add(fmt.Sprintf("c%d", i))
	}
	s, err := a.Snapshot("apim", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(s); err != nil {
		t.Fatalf("50000 rows with the default limit: %v", err)
	}
	add("one-too-many")
	if s, err = a.Snapshot("apim", "1.4.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(s); !errors.Is(err, ErrTooLarge) {
		t.Errorf("50001 rows with the default limit: err = %v, want ErrTooLarge", err)
	}
}
