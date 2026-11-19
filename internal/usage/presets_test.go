package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// parse runs a preset over the input and collects what it emits.
func parse(t *testing.T, preset, input string, opts ParseOptions) ([]Record, int64, error) {
	t.Helper()
	got, res, err := parseResult(t, preset, input, opts)
	return got, res.Skipped, err
}

// parseResult runs a preset over the input and collects what it emits, with the whole result.
func parseResult(t *testing.T, preset, input string, opts ParseOptions) ([]Record, ParseResult, error) {
	t.Helper()
	p, ok := PresetByName(preset)
	if !ok {
		t.Fatalf("no preset %q", preset)
	}
	var got []Record
	res, err := p.Parse(strings.NewReader(input), opts, func(r Record) { got = append(got, r) })
	return got, res, err
}

// parseFixture parses a file of testdata and fails the test on an error or a skipped line.
func parseFixture(t *testing.T, preset, file string, opts ParseOptions) []Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	got, skipped, err := parse(t, preset, string(data), opts)
	if err != nil || skipped != 0 {
		t.Fatalf("%s: skipped = %d, err = %v", file, skipped, err)
	}
	return got
}

func assertRecords(t *testing.T, got, want []Record) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("records =\n%s\nwant\n%s", dump(got), dump(want))
	}
}

func dump(rs []Record) string {
	var b strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&b, "  %+v\n", r)
	}
	return b.String()
}

func TestPresets_registry(t *testing.T) {
	if got, want := PresetNames(), []string{"apim", "appinsights", "datadog", "generic", "kong", "nginx", "otel"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PresetNames() = %v, want %v", got, want)
	}
	for _, name := range PresetNames() {
		p, ok := PresetByName(name)
		if !ok || p.Name != name || p.Parse == nil || p.Query == "" {
			t.Errorf("PresetByName(%q) = %+v, %v", name, p, ok)
		}
	}
	kinds := map[string]string{"generic": "", "apim": "client", "appinsights": "service", "datadog": "service", "kong": "client",
		"nginx": "client", "otel": "service"}
	for name, kind := range kinds {
		if p, _ := PresetByName(name); p.DefaultKind != kind {
			t.Errorf("%s DefaultKind = %q, want %q", name, p.DefaultKind, kind)
		}
	}
}

func TestPresetByName_aliases(t *testing.T) {
	aliases := map[string]string{
		"app-insights": "appinsights", "applicationinsights": "appinsights", "application-insights": "appinsights",
		"azure-apim": "apim", "api-management": "apim",
		"opentelemetry": "otel", "otlp": "otel",
		"dd":  "datadog",
		"csv": "generic", "json": "generic",
	}
	for alias, canonical := range aliases {
		for _, name := range []string{alias, strings.ToUpper(alias), "  " + alias + " "} {
			p, ok := PresetByName(name)
			if !ok || p.Name != canonical {
				t.Errorf("PresetByName(%q) = %q, %v; want %q", name, p.Name, ok, canonical)
			}
		}
	}
	for _, name := range []string{"APIM", " Datadog ", "AppInsights"} {
		if p, ok := PresetByName(name); !ok || !strings.EqualFold(p.Name, strings.TrimSpace(name)) {
			t.Errorf("PresetByName(%q) = %q, %v", name, p.Name, ok)
		}
	}
	for _, name := range []string{"", "datadog-apm", "app insights", "ddog"} {
		if _, ok := PresetByName(name); ok {
			t.Errorf("PresetByName(%q) found a preset", name)
		}
	}
	// Aliases are accepted, never listed.
	if got, want := PresetNames(), []string{"apim", "appinsights", "datadog", "generic", "kong", "nginx", "otel"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PresetNames() = %v, want %v", got, want)
	}
}

func TestPresets_query_texts(t *testing.T) {
	appinsights := `// Azure Application Insights (workspace-based): outgoing HTTP calls of your services, last 7 days.
// Run with:  az monitor log-analytics query --workspace <workspace-id> --analytics-query "<this query>" -o json
AppDependencies
| where TimeGenerated > ago(7d) and DependencyType startswith "http"
| extend u = parse_url(Data)
| extend Host = tolower(iff(isempty(tostring(u.Host)), Target, tostring(u.Host))),
         Path = strcat("/", trim_start(@"/", tostring(u.Path))),
         Method = extract(@"^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s", 1, Name)
| summarize Calls = sum(ItemCount), LastSeen = max(TimeGenerated) by AppRoleName, Host, Method, Path
`
	datadog := `# Datadog APM: count outgoing HTTP calls of one service, last 7 days.
# Run once per calling service (replace orders); append every answer to one file.
curl -s -w '\n' -X POST "https://api.${DD_SITE:-datadoghq.com}/api/v2/spans/analytics/aggregate" \
  -H "Content-Type: application/json" \
  -H "DD-API-KEY: ${DD_API_KEY}" -H "DD-APPLICATION-KEY: ${DD_APP_KEY}" \
  -d '{"data":{"type":"aggregate_request","attributes":{
    "filter":{"from":"now-7d","to":"now","query":"service:orders @span.kind:client"},
    "compute":[{"aggregation":"count","type":"total"}],
    "group_by":[{"facet":"service","limit":1},
      {"facet":"@http.url_details.host","limit":20},
      {"facet":"@http.url_details.path","limit":100},
      {"facet":"@http.method","limit":5}]}}}' >> datadog-usage.json
`
	apim := `ApiManagementGatewayLogs
| where TimeGenerated > ago(7d)
| extend u = parse_url(Url)
| extend Path = strcat("/", trim_start(@"/", tostring(u.Path))), Host = tostring(u.Host)
| summarize Calls = count(), LastSeen = max(TimeGenerated) by ApimSubscriptionId, ApiId, Method, Host, Path
`
	otel := `processors:
  filter/http_client:
    error_mode: ignore
    trace_conditions:
      - span.kind != SPAN_KIND_CLIENT
      - span.attributes["http.request.method"] == nil and span.attributes["http.method"] == nil
exporters:
  file/http_client:
    path: /var/otel/http-client.jsonl
    rotation: {max_megabytes: 100, max_backups: 10}
service:
  pipelines:
    traces/http_client: {receivers: [otlp], processors: [filter/http_client], exporters: [file/http_client]}
`
	kong := `# Kong: log requests with the file-log plugin, then send the file.
# Each line is one JSON object; Graphwake reads consumer, request.method, request.uri,
# request.headers.host (or the host in request.url) and started_at, and nothing else,
# except the field you name with --caller-field.
plugins:
  - name: file-log
    config:
      path: /var/log/kong/usage.log
`
	nginx := `# NGINX: add this log format (http context), then send the file.
# "caller" is the value that identifies the client: a header you set at the edge,
# $remote_user for Basic auth, or $ssl_client_s_dn for mutual TLS.
log_format graphwake_usage escape=json
  '{"time":"$time_iso8601","method":"$request_method",'
  '"host":"$host","path":"$uri","caller":"$http_x_client_id"}';
access_log /var/log/nginx/usage.log graphwake_usage;
`
	for name, want := range map[string]string{"apim": apim, "appinsights": appinsights, "datadog": datadog, "kong": kong, "nginx": nginx, "otel": otel} {
		if p, _ := PresetByName(name); p.Query != want {
			t.Errorf("%s query =\n%s\nwant\n%s", name, p.Query, want)
		}
	}

	// The generic text is the CSV header and one example line, and the generic preset reads it back.
	p, _ := PresetByName("generic")
	lines := strings.Split(strings.TrimSuffix(p.Query, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "caller,callerKind,service,method,path,count,lastSeen" {
		t.Fatalf("generic query = %q", p.Query)
	}
	got, skipped, err := parse(t, "generic", p.Query, ParseOptions{})
	if err != nil || skipped != 0 || len(got) != 1 {
		t.Errorf("generic example: records = %v, skipped = %d, err = %v", got, skipped, err)
	}
}

const marker = "SECRET-MARKER-8812"

// badLineCase is one input with broken lines or elements between good ones, and what a preset makes of it.
type badLineCase struct {
	name, preset, input string
	records             int
	skipped             int64
}

// badLineCases are the inputs of TestParsers_skip_bad_lines_and_count; TestParsers_chunked_reads_match_plain reads
// them again in small pieces.
func badLineCases() []badLineCase {
	apimRow := `{"ApimSubscriptionId":"checkout-web","ApiId":"orders-api","Method":"GET","Host":"gateway.example","Path":"/orders/1","Calls":"2","LastSeen":"2026-11-18T09:12:44Z","TableName":"PrimaryResult"}`
	otelLine := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout-web"}}]},"scopeSpans":[{"spans":[{"kind":3,"startTimeUnixNano":"1794993164000000000","attributes":[{"key":"http.request.method","value":{"stringValue":"GET"}},{"key":"url.full","value":{"stringValue":"https://orders.internal/v1/orders/1"}}]}]}]}]}`
	kongRow := `{"consumer":{"username":"checkout-web"},"request":{"headers":{"host":"gateway.example"},"uri":"/orders/1","method":"GET","querystring":{}},"started_at":1794993164000}`
	nginxRow := `{"time":"2026-11-18T09:12:44+00:00","method":"GET","host":"gateway.example","path":"/orders/1","caller":"checkout-web"}`
	aiRow := `{"AppRoleName":"checkout-web","Host":"orders.internal","Method":"GET","Path":"/orders/1","Calls":"2","LastSeen":"2026-11-18T09:12:44Z","TableName":"PrimaryResult"}`
	aiTables := `{"tables":[{"name":"PrimaryResult","columns":[{"name":"AppRoleName"},{"name":"Host"},{"name":"Method"},{"name":"Path"},{"name":"Calls"},{"name":"LastSeen"}],"rows":[`
	aiGood := `["checkout-web","orders.internal","GET","/orders/1",2,"2026-11-18T09:12:44Z"]`
	aiDoc := aiTables + aiGood + `]}]}`
	ddBucket := `{"type":"bucket","attributes":{"by":{"service":"checkout-web","@http.url_details.host":"orders.internal","@http.url_details.path":"/orders/1","@http.method":"GET"},"compute":{"c0":2}}}`
	ddDoc := `{"data":[` + ddBucket + `],"meta":{"status":"done"}}`
	garbage := marker + ` not a record {"x":`
	errorLine := `[error] something went wrong ` + marker
	return []badLineCase{
		{"generic csv", "generic", "caller,method,path,lastSeen\na,GET,/x,2026-11-18\n" + garbage + "\nb,GET,/y,2026-11-18\n", 2, 1},
		{"generic csv bad values", "generic", "caller,callerKind,method,path,count,lastSeen\n" +
			"a,client,GET,/x,1,2026-11-18\n" +
			"b,robot,GET,/x,1,2026-11-18\n" + // unknown caller kind
			"c,client,GET,/x,-4,2026-11-18\n" + // negative count
			"d,client,GET,/x,many,2026-11-18\n" + // count not a number
			"e,client,GET,x,1,2026-11-18\n" + // path without a leading slash
			"f,client,,/x,1,2026-11-18\n" + // no method
			"g,client,GET,/x,1,yesterday\n" + // not a date
			"h,client,GET,/x,1,2026-11-18\n", 2, 6},
		{"generic csv unclosed quote", "generic", "caller,method,path,lastSeen\na,GET,/x,2026-11-18\n\"" + marker + ",GET,/x,2026-11-18\nc,GET,/x,2026-11-18\n\"d, quoted\",GET,/x,2026-11-18\ne,GET,/x,2026-11-18\n", 4, 1},
		{"generic json lines", "generic", `{"caller":"a","method":"GET","path":"/x","lastSeen":"2026-11-18"}` + "\n" + garbage + "\n" + `{"caller":"b","method":"GET","path":"/y","lastSeen":"2026-11-18"}`, 2, 1},
		{"generic json lines with an [error] line", "generic", `{"caller":"a","method":"GET","path":"/x","lastSeen":"2026-11-18"}` + "\n" + errorLine + "\n" + `{"caller":"b","method":"GET","path":"/y","lastSeen":"2026-11-18"}`, 2, 1},
		{"generic json array", "generic", `[{"caller":"a","method":"GET","path":"/x","lastSeen":"2026-11-18"}, "` + marker + `", {"caller":"b","method":{"x":1},"path":"/y","lastSeen":"2026-11-18"}, {"caller":"c","method":"GET","path":"/y","lastSeen":"2026-11-18"}]`, 2, 2},
		{"apim json lines", "apim", apimRow + "\n" + garbage + "\n" + apimRow + "\n", 2, 1},
		{"apim array", "apim", "[\n" + apimRow + ",\n42,\n\"" + marker + "\",\n" + strings.Replace(apimRow, `"Calls":"2"`, `"Calls":"`+marker+`"`, 1) + ",\n" + apimRow + "\n]", 2, 3},
		// The broken line and the "]" left over are skipped; the rows before and after it are read.
		{"apim broken array keeps what came before and after", "apim", "[\n" + apimRow + ",\n" + garbage + "\n" + apimRow + "\n]", 2, 2},
		{"apim lines with an [error] line", "apim", apimRow + "\n" + errorLine + "\n" + apimRow + "\n", 2, 1},
		{"otel", "otel", otelLine + "\n" + garbage + "\n" + otelLine + "\n", 2, 1},
		{"otel json array", "otel", "[" + otelLine + ",\n42,\n" + otelLine + "]", 2, 1},
		{"otel with an [error] line", "otel", otelLine + "\n" + errorLine + "\n" + otelLine + "\n", 2, 1},
		{"kong file-log with an [error] line", "kong", kongRow + "\n" + errorLine + "\n" + kongRow + "\n", 2, 1},
		{"kong file-log lines", "kong", kongRow + "\n" + garbage + "\n" + kongRow + "\n" + strings.Replace(kongRow, `"started_at":1794993164000`, `"started_at":"`+marker+`"`, 1) + "\n", 2, 2},
		{"kong http-log array", "kong", "[\n" + kongRow + ",\n42,\n\"" + marker + "\",\n" + strings.Replace(kongRow, `"/orders/1"`, `"`+marker+`"`, 1) + ",\n" + kongRow + "\n]", 2, 3},
		{"nginx", "nginx", `["` + marker + `"]` + "\n" + nginxRow + "\n" + garbage + "\n" + nginxRow + "\n" + strings.Replace(nginxRow, `"GET"`, `""`, 1) + "\n", 2, 3},
		{"appinsights log analytics array", "appinsights", "[\n" + aiRow + ",\n42,\n\"" + marker + "\",\n" + strings.Replace(aiRow, `"Calls":"2"`, `"Calls":"`+marker+`"`, 1) + ",\n" + aiRow + "\n]", 2, 3},
		{"appinsights lines with an [error] line", "appinsights", aiRow + "\n" + errorLine + "\n" + aiRow + "\n", 2, 1},
		{"appinsights lines with a garbage line", "appinsights", aiRow + "\n" + garbage + "\n" + aiRow + "\n", 2, 1},
		{"appinsights tables with bad rows", "appinsights", aiTables + aiGood + `,["` + marker + `"],42,"` + marker + `",` +
			`["` + marker + `","orders.internal","GET","/orders/1","` + marker + `","2026-11-18T09:12:44Z"],` +
			`["a","orders.internal","GET","/orders/1",{"n":1},"2026-11-18T09:12:44Z"],` + aiGood + `]}]}`, 2, 5},
		{"appinsights tables with an [error] line", "appinsights", aiDoc + "\n" + errorLine + "\n" + aiDoc + "\n", 2, 1},
		// A document cut short costs one skipped value; the good one on the next line is read.
		{"appinsights tables cut short, then a good one", "appinsights", aiTables + aiGood + "\n" + aiDoc + "\n", 2, 1},
		{"appinsights tables cut inside a string", "appinsights", `{"tables":[{"name":"Prim` + marker + "\n" + aiDoc, 1, 1},
		{"appinsights tables cut at the end", "appinsights", aiDoc + "\n" + aiTables + aiGood, 2, 1},
		{"datadog answers with a garbage line", "datadog", ddDoc + "\n" + garbage + "\n" + ddDoc + "\n", 2, 1},
		{"datadog answers with an [error] line", "datadog", ddDoc + "\n" + errorLine + "\n" + ddDoc, 2, 1},
		{"datadog bad buckets", "datadog", `{"data":[` + ddBucket + `,42,"` + marker + `",` +
			strings.Replace(ddBucket, `"c0":2`, `"c0":"`+marker+`"`, 1) + `,` +
			strings.Replace(ddBucket, `"/orders/1"`, `"`+marker+`"`, 1) + `,` + ddBucket + `],"meta":{"status":"done"}}`, 2, 4},
		{"datadog error answer", "datadog", `{"errors":["` + marker + `"]}` + "\n" + ddDoc + ddDoc, 2, 1},
		{"datadog answer cut short, then a good one", "datadog", `{"data":[` + ddBucket + "\n" + ddDoc + "\n", 2, 1},
		{"datadog answer cut inside a string", "datadog", `{"data":[{"type":"buck` + marker + "\n" + ddDoc, 1, 1},
		{"datadog answer cut at the end", "datadog", ddDoc + "\n" + `{"data":[` + ddBucket, 2, 1},
	}
}

// Every preset skips a broken line or element between good ones, counts it, and never puts its content in an error.
func TestParsers_skip_bad_lines_and_count(t *testing.T) {
	for _, c := range badLineCases() {
		t.Run(c.name, func(t *testing.T) {
			got, skipped, err := parse(t, c.preset, c.input, ParseOptions{})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != c.records || skipped != c.skipped {
				t.Errorf("records = %d, skipped = %d; want %d, %d\n%s", len(got), skipped, c.records, c.skipped, dump(got))
			}
			for _, r := range got {
				if strings.Contains(fmt.Sprint(r), marker) {
					t.Errorf("record carries the bad line's content: %+v", r)
				}
			}
		})
	}
}

func TestSplitCSVLine(t *testing.T) {
	cases := []struct {
		line string
		want []string // nil = broken
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{"a,,c,", []string{"a", "", "c", ""}},
		{`"mobile app, ios",GET`, []string{"mobile app, ios", "GET"}},
		{`"say ""hi""", next`, []string{`say "hi"`, "next"}},
		{`a, "b" ,c`, []string{"a", "b", "c"}},
		{`"unclosed,GET,/x`, nil},
		{`a"b,c`, nil},
		{`"a"b,c`, nil},
	}
	for _, c := range cases {
		got, ok := splitCSVLine(c.line)
		if ok != (c.want != nil) || !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCSVLine(%q) = %q, %v; want %q", c.line, got, ok, c.want)
		}
	}
}

func TestHostName(t *testing.T) {
	for raw, want := range map[string]string{
		"Gateway.Example:443":          "gateway.example",
		"orders.internal":              "orders.internal",
		"user:pw@Orders.Internal:8080": "orders.internal",
		"user@orders.internal":         "orders.internal",
		"[::1]:8080":                   "::1",
		"":                             "",
	} {
		if got := hostName(raw); got != want {
			t.Errorf("hostName(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Input with content but not one readable line or element is an error; its text never echoes the input.
func TestParsers_unreadable_input_is_error(t *testing.T) {
	garbage := marker + " ?token=abc\n" + marker + " {\"x\":1\n"
	for _, preset := range PresetNames() {
		got, skipped, err := parse(t, preset, garbage, ParseOptions{})
		if err == nil || len(got) != 0 {
			t.Errorf("%s: records = %d, skipped = %d, err = %v; want an error", preset, len(got), skipped, err)
			continue
		}
		if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "token") {
			t.Errorf("%s: error echoes the input: %v", preset, err)
		}
	}
	// Empty input is not an error: there is nothing to read.
	for _, preset := range PresetNames() {
		if got, skipped, err := parse(t, preset, " \n\n", ParseOptions{}); err != nil || skipped != 0 || len(got) != 0 {
			t.Errorf("%s empty: records = %d, skipped = %d, err = %v", preset, len(got), skipped, err)
		}
	}
}

type failingReader struct{ data io.Reader }

var errDisk = errors.New("disk on fire")

func (f *failingReader) Read(p []byte) (int, error) {
	n, err := f.data.Read(p)
	if err == io.EOF {
		return n, errDisk
	}
	return n, err
}

// A read error is never taken for the end of the input, even after records were read.
func TestParsers_read_error_is_returned(t *testing.T) {
	inputs := map[string]string{
		"generic": "caller,method,path,lastSeen\na,GET,/x,2026-11-18\n",
		"apim":    `[{"Method":"GET","Path":"/x","Calls":"1","LastSeen":"2026-11-18T00:00:00Z"},`,
		"otel":    `{"resourceSpans":[]}` + "\n",
		"kong":    `[{"request":{"uri":"/x","method":"GET"},"started_at":1794993164000},`,
		"nginx":   `{"time":"2026-11-18T09:12:44+00:00","method":"GET","path":"/x"}` + "\n",
		"appinsights": `{"tables":[{"columns":[{"name":"Method"},{"name":"Path"},{"name":"Calls"},{"name":"LastSeen"}],` +
			`"rows":[["GET","/x",1,"2026-11-18T00:00:00Z"],`,
		"datadog": `{"data":[{"attributes":{"by":{"@http.method":"GET","@http.url_details.path":"/x"},"compute":{"c0":1}}},`,
	}
	for preset, input := range inputs {
		p, _ := PresetByName(preset)
		n := 0
		_, err := p.Parse(&failingReader{strings.NewReader(input)}, ParseOptions{}, func(Record) { n++ })
		if !errors.Is(err, errDisk) {
			t.Errorf("%s: err = %v, want the read error", preset, err)
		}
		if (preset == "appinsights" || preset == "datadog") && n != 1 {
			t.Errorf("%s: %d records before the read error, want 1", preset, n)
		}
	}
}

// Every preset streams: the first record comes out before the writer has written the rest, and 200,000 lines finish.
func TestParsers_stream_large_input(t *testing.T) {
	const total = 200000
	cases := []struct {
		preset           string
		head, line, tail string // head and the first line form the first write
		sep              string // written before every line after the first
	}{
		{"generic", "caller,callerKind,service,method,path,count,lastSeen\n", "checkout-web,client,orders,GET,/v1/orders/%d,1,2026-11-18\n", "", ""},
		{"apim", "[\n", `{"ApimSubscriptionId":"checkout-web","ApiId":"orders-api","Method":"GET","Host":"gateway.example","Path":"/orders/%d","Calls":"1","LastSeen":"2026-11-18T09:12:44Z","TableName":"PrimaryResult"}`, "\n]\n", ",\n"},
		{"kong", "", `{"consumer":{"username":"checkout-web"},"request":{"headers":{"host":"gateway.example","user-agent":"checkout-web/2.4"},"uri":"/orders/%d?expand=lines","method":"GET","querystring":{"expand":"lines"}},"started_at":1794993164000}` + "\n", "", ""},
		{"nginx", "", `{"time":"2026-11-18T09:12:44+00:00","method":"GET","host":"gateway.example","path":"/orders/%d","caller":"checkout-web"}` + "\n", "", ""},
		{"appinsights", `{"tables":[{"name":"PrimaryResult","columns":[{"name":"AppRoleName","type":"string"},{"name":"Host","type":"string"},` +
			`{"name":"Method","type":"string"},{"name":"Path","type":"string"},{"name":"Calls","type":"long"},{"name":"LastSeen","type":"datetime"}],"rows":[` + "\n",
			`["checkout-web","orders.internal","GET","/orders/%d",1,"2026-11-18T09:12:44Z"]`, "\n]}]}\n", ",\n"},
		{"datadog", `{"data":[`, `{"type":"bucket","attributes":{"by":{"service":"checkout-web","@http.url_details.host":"orders.internal",` +
			`"@http.url_details.path":"/orders/%d","@http.method":"GET"},"compute":{"c0":1}}}`, `],"meta":{"status":"done","elapsed":1200}}`, ","},
		{"otel", "", `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout-web"}}]},"scopeSpans":[{"spans":[{"kind":3,"startTimeUnixNano":"1794993164000000000","attributes":[{"key":"http.request.method","value":{"stringValue":"GET"}},{"key":"url.full","value":{"stringValue":"https://orders.internal/v1/orders/%d"}}]}]}]}]}` + "\n", "", ""},
	}
	for _, c := range cases {
		t.Run(c.preset, func(t *testing.T) {
			p, _ := PresetByName(c.preset)
			pr, pw := io.Pipe()
			firstEmit := make(chan struct{})
			timedOut := make(chan struct{})
			go func() {
				if _, err := io.WriteString(pw, c.head+fmt.Sprintf(c.line, 0)); err != nil {
					return
				}
				select {
				case <-firstEmit:
				case <-time.After(10 * time.Second):
					close(timedOut)
					pw.CloseWithError(errors.New("no record before the rest of the input"))
					return
				}
				var b strings.Builder
				for i := 1; i < total; i++ {
					b.WriteString(c.sep)
					fmt.Fprintf(&b, c.line, i)
					if b.Len() > 64<<10 {
						if _, err := io.WriteString(pw, b.String()); err != nil {
							return
						}
						b.Reset()
					}
				}
				b.WriteString(c.tail)
				if _, err := io.WriteString(pw, b.String()); err != nil {
					return
				}
				pw.Close()
			}()

			var n int
			res, err := p.Parse(pr, ParseOptions{}, func(Record) {
				if n == 0 {
					close(firstEmit)
				}
				n++
			})
			pr.Close()
			select {
			case <-timedOut:
				t.Fatal("the first record was not emitted before the rest of the input was written")
			default:
			}
			if err != nil || res.Skipped != 0 || n != total {
				t.Errorf("records = %d, skipped = %d, err = %v; want %d, 0, nil", n, res.Skipped, err, total)
			}
		})
	}
}

// eachJSONValue reads every top-level value of the input: arrays one after another, with or without line breaks
// between them, and arrays and lines mixed.
func TestEachJSONValue_every_top_level_value(t *testing.T) {
	cases := []struct {
		name, input   string
		want          []string // the values fn saw, in order
		read, skipped int64
	}{
		{"arrays one after another", `[{"n":1},{"n":2}][{"n":3}]`, []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, 3, 0},
		{"arrays on separate lines", "[{\"n\":1}]\n\n[\n  {\"n\":2}\n]\n", []string{`{"n":1}`, `{"n":2}`}, 2, 0},
		{"lines and arrays mixed", "{\"n\":1}\n[{\"n\":2},{\"n\":3}]\n{\"n\":4}\n[]\n{\"n\":5}", []string{`{"n":1}`, `{"n":2}`, `{"n":3}`, `{"n":4}`, `{"n":5}`}, 5, 0},
		{"a line right after an array", "[{\"n\":1}] {\"n\":2}\n{\"n\":3}", []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, 3, 0},
		{"a bad element costs the rest of its line", "[{\"n\":1}]\n[{\"n\":2}, oops {\"n\":9}\n{\"n\":3}\n", []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, 3, 1},
		{"an [error] line between lines", "{\"n\":1}\n[error] something went wrong\n{\"n\":2}\n", []string{`{"n":1}`, `{"n":2}`}, 2, 1},
		{"an [error] line between arrays", "[{\"n\":1}]\n[error] something [went] wrong\n[{\"n\":2}]", []string{`{"n":1}`, `{"n":2}`}, 2, 1},
		{"a line cut short after an element", "[{\"n\":1},{\"n\":2}\n{\"n\":3}\n", []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}, 3, 0},
		{"a line cut short inside an element", "[{\"n\":1},{\"n\n{\"n\":3}\n[{\"n\":4}]", []string{`{"n":1}`, `{"n":3}`, `{"n":4}`}, 3, 1},
		{"a broken pretty-printed array", "[\n  {\"n\":1},\n  {\"n\": oops},\n  {\"n\":2}\n]\n{\"n\":3}", []string{`{"n":1}`, `{"n": oops},`, `{"n":2}`, `]`, `{"n":3}`}, 3, 2},
		{"a later array cut short", "[{\"n\":1}]\n[{\"n\":2}", []string{`{"n":1}`, `{"n":2}`}, 2, 1},
		{"a bad line between arrays", "[{\"n\":1}]\nnot json\n[{\"n\":2}]", []string{`{"n":1}`, "not json", `{"n":2}`}, 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br, first, ok, err := openInput(strings.NewReader(c.input))
			if err != nil || !ok {
				t.Fatalf("openInput: ok = %v, err = %v", ok, err)
			}
			var got []string
			read, skipped, err := eachJSONValue(br, first, func(raw []byte) bool {
				got = append(got, string(raw))
				return raw[0] == '{' && json.Valid(raw)
			})
			if err != nil || read != c.read || skipped != c.skipped || !reflect.DeepEqual(got, c.want) {
				t.Errorf("values = %q, read = %d, skipped = %d, err = %v; want %q, %d, %d", got, read, skipped, err, c.want, c.read, c.skipped)
			}
		})
	}

	// Many arrays, each larger than the read buffers, separated in every way: every element, in order.
	var b strings.Builder
	n := 0
	for a := 0; a < 60; a++ {
		b.WriteString("[")
		for e := 0; e < 300; e++ {
			if e > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"n":%d,"pad":"%s"}`, n, strings.Repeat("x", a%7*10))
			n++
		}
		b.WriteString([]string{"]", "]\n", "]\r\n\n", "] "}[a%4])
	}
	br, first, _, _ := openInput(strings.NewReader(b.String()))
	next := 0
	read, skipped, err := eachJSONValue(br, first, func(raw []byte) bool {
		if want := fmt.Sprintf(`{"n":%d,`, next); !strings.HasPrefix(string(raw), want) {
			t.Fatalf("element %d = %.40s", next, raw)
		}
		next++
		return true
	})
	if err != nil || read != int64(n) || skipped != 0 {
		t.Errorf("read = %d, skipped = %d, err = %v; want %d, 0, nil", read, skipped, err, n)
	}
}

// eachJSONObject reads every object of a long input in order: many objects, each larger than the read buffers,
// separated by nothing, a line break, blank lines or a space (curl appends answers without a newline).
func TestEachJSONObject_many_large_objects(t *testing.T) {
	var b strings.Builder
	n := 0
	for a := 0; a < 60; a++ {
		b.WriteString(`{"data":[`)
		for e := 0; e < 300; e++ {
			if e > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"n":%d,"pad":"%s"}`, n, strings.Repeat("x", a%7*10))
			n++
		}
		b.WriteString(`],"meta":{"status":"done"}}`)
		b.WriteString([]string{"", "\n", "\r\n\n", " "}[a%4])
	}
	br, first, _, _ := openInput(strings.NewReader(b.String()))
	next := 0
	read, skipped, err := eachJSONObject(br, first, func(s *jsonStream, count func(bool)) error {
		if _, err := s.Token(); err != nil { // '{'
			return err
		}
		for s.More() {
			key, err := s.Key()
			if err != nil {
				return err
			}
			if key != "data" {
				if err = s.SkipValue(); err != nil {
					return err
				}
				continue
			}
			if _, err = s.Token(); err != nil { // '['
				return err
			}
			for s.More() {
				raw, err := s.Value()
				if err != nil {
					return err
				}
				if want := fmt.Sprintf(`{"n":%d,`, next); !strings.HasPrefix(string(raw), want) {
					t.Fatalf("element %d = %.40s", next, raw)
				}
				next++
				count(true)
			}
			if _, err = s.Token(); err != nil { // ']'
				return err
			}
		}
		_, err := s.Token() // '}'
		return err
	})
	if err != nil || read != int64(n) || skipped != 0 || next != n {
		t.Errorf("read = %d, skipped = %d, err = %v; want %d, 0, nil", read, skipped, err, n)
	}
}

// chunkReader hands out at most n bytes per Read.
type chunkReader struct {
	r io.Reader
	n int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(p) > c.n {
		p = p[:c.n]
	}
	return c.r.Read(p)
}

// How the reader hands out the input never changes the result: the array and object readers resync the same way
// whether the decoder sees the input in one piece, one byte at a time or in small chunks.
func TestParsers_chunked_reads_match_plain(t *testing.T) {
	type input struct{ name, preset, text string }
	var inputs []input
	for _, c := range badLineCases() {
		inputs = append(inputs, input{c.name, c.preset, c.input})
	}
	for _, f := range []struct{ preset, file string }{
		{"apim", "apim-log-analytics.json"}, {"kong", "kong-http-log-batch.json"}, {"appinsights", "appinsights-tables.json"},
		{"appinsights", "appinsights-log-analytics.json"}, {"datadog", "datadog-aggregate.json"},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", f.file))
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, input{f.file, f.preset, string(data)})
	}
	b := `{"type":"bucket","attributes":{"by":{"service":"s","@http.url_details.path":"/v1/orders/1","@http.method":"GET"},"compute":{"c0":1}}}`
	good := `{"data":[` + b + `],"meta":{"status":"done"}}`
	pretty := "{\n  \"data\": [\n    " + b + ",\n    " + b + "\n  ],\n  \"meta\": {\"status\": \"done\"}\n}\n"
	for name, text := range map[string]string{
		"cut inside a string":   `{"data":[` + b + `,{"type":"buck` + "\n" + good + "\n" + good,
		"cut after a bucket":    `{"data":[` + b + "\n" + good + "\n" + good,
		"cut after a comma":     `{"data":[` + b + ",\n" + good + good,
		"cut after the bracket": `{"data":[` + "\n" + good,
		"cut pretty-printed":    pretty[:len(pretty)/2] + "\n" + pretty + good,
		"answers on one line":   good + good + " " + good,
		"input ends inside":     good + "\n" + `{"data":[` + b,
	} {
		inputs = append(inputs, input{"datadog " + name, "datadog", text})
	}
	inputs = append(inputs,
		input{"apim arrays", "apim", "[{\"Method\":\"GET\",\"Path\":\"/x\",\"LastSeen\":\"2026-11-18\"},\n oops\n{\"Method\":\"GET\",\"Path\":\"/y\",\"LastSeen\":\"2026-11-18\"}]" +
			"[{\"Method\":\"GET\",\"Path\":\"/z\",\"LastSeen\":\"2026-11-18\"},{\"Method\":\"GET\",\"Path\":\"/w\",\"LastSeen\":\"2026-11-18\"}\n{\"Method\":\"GET\",\"Path\":\"/v\",\"LastSeen\":\"2026-11-18\"}\n"})

	type outcome struct {
		records []Record
		res     ParseResult
		err     string
	}
	run := func(preset string, r io.Reader) outcome {
		p, _ := PresetByName(preset)
		var o outcome
		res, err := p.Parse(r, ParseOptions{Now: ddClock}, func(rec Record) { o.records = append(o.records, rec) })
		o.res = res
		if err != nil {
			o.err = err.Error()
		}
		return o
	}
	readers := map[string]func(string) io.Reader{
		"one byte": func(s string) io.Reader { return iotest.OneByteReader(strings.NewReader(s)) },
		"3 bytes":  func(s string) io.Reader { return &chunkReader{strings.NewReader(s), 3} },
		"7 bytes":  func(s string) io.Reader { return &chunkReader{strings.NewReader(s), 7} },
		"half":     func(s string) io.Reader { return iotest.HalfReader(strings.NewReader(s)) },
	}
	for _, in := range inputs {
		want := run(in.preset, strings.NewReader(in.text))
		for rname, reader := range readers {
			if got := run(in.preset, reader(in.text)); !reflect.DeepEqual(got, want) {
				t.Errorf("%s, %s: %+v\nwant %+v", in.name, rname, got, want)
			}
		}
	}
}

// repeatReader streams n copies of one byte without holding them.
type repeatReader struct {
	c byte
	n int64
}

func (r *repeatReader) Read(b []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > r.n {
		b = b[:r.n]
	}
	for i := range b {
		b[i] = r.c
	}
	r.n -= int64(len(b))
	return len(b), nil
}

// A line longer than maxLineBytes is skipped and counted as unreadable without being held whole, and reading goes on
// at the next line; a line of exactly maxLineBytes is still read. The long lines are streamed, never built in memory.
func TestParsers_line_over_the_cap_is_skipped(t *testing.T) {
	const nginxGood = `{"time":"2026-11-18T10:00:00+00:00","method":"GET","host":"orders.internal","path":"/v1/orders/1","caller":"checkout-web"}`
	const kongGood = `{"consumer":{"username":"checkout-web"},"request":{"headers":{"host":"gateway.example"},"uri":"/orders/1","method":"GET"},"started_at":1794993164000}`
	const csvHeader = "caller,method,path,lastSeen\n"
	const csvGood = "checkout-web,GET,/orders/1,2026-11-18"
	cases := []struct {
		name, preset, before, open, close, after string
	}{
		{"nginx", "nginx", "", `{"time":"2026-11-18T10:00:00+00:00","method":"GET","host":"orders.internal","path":"/v1/orders/1","caller":"checkout-web","pad":"`, `"}`, nginxGood},
		{"kong lines", "kong", "", `{"pad":"`, `","consumer":{"username":"checkout-web"},"request":{"headers":{"host":"gateway.example"},"uri":"/orders/1","method":"GET"},"started_at":1794993164000}`, kongGood},
		{"generic JSON lines", "generic", "", `{"caller":"checkout-web","method":"GET","path":"/orders/1","lastSeen":"2026-11-18","pad":"`, `"}`, `{"caller":"checkout-web","method":"GET","path":"/orders/1","lastSeen":"2026-11-18"}`},
		{"generic CSV", "generic", csvHeader, "checkout-web,GET,/orders/", ",2026-11-18", csvGood},
	}
	for _, tc := range cases {
		p, _ := PresetByName(tc.preset)
		for _, extra := range []int64{0, 1} { // a line of exactly the cap, then one byte over
			pad := maxLineBytes + extra - int64(len(tc.open)+len(tc.close))
			in := io.MultiReader(strings.NewReader(tc.before+tc.open), &repeatReader{c: 'a', n: pad},
				strings.NewReader(tc.close+"\n"+tc.after+"\n"))
			var records int
			res, err := p.Parse(in, ParseOptions{}, func(Record) { records++ })
			wantRecords, wantSkipped := 2, int64(0)
			if extra > 0 {
				wantRecords, wantSkipped = 1, 1
			}
			if err != nil || records != wantRecords || res.Skipped != wantSkipped {
				t.Errorf("%s, line of cap+%d: %d records, %d skipped, err %v; want %d, %d", tc.name, extra, records,
					res.Skipped, err, wantRecords, wantSkipped)
			}
		}
	}
}
