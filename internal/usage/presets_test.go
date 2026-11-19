package usage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// parse runs a preset over the input and collects what it emits.
func parse(t *testing.T, preset, input string, opts ParseOptions) ([]Record, int64, error) {
	t.Helper()
	p, ok := PresetByName(preset)
	if !ok {
		t.Fatalf("no preset %q", preset)
	}
	var got []Record
	skipped, err := p.Parse(strings.NewReader(input), opts, func(r Record) { got = append(got, r) })
	return got, skipped, err
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
	if got, want := PresetNames(), []string{"apim", "generic", "otel"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PresetNames() = %v, want %v", got, want)
	}
	for _, name := range PresetNames() {
		p, ok := PresetByName(name)
		if !ok || p.Name != name || p.Parse == nil || p.Query == "" {
			t.Errorf("PresetByName(%q) = %+v, %v", name, p, ok)
		}
	}
	if _, ok := PresetByName("kong"); ok {
		t.Error("PresetByName(kong) found a preset; Kong is Plan 2")
	}
	kinds := map[string]string{"generic": "", "apim": "client", "otel": "service"}
	for name, kind := range kinds {
		if p, _ := PresetByName(name); p.DefaultKind != kind {
			t.Errorf("%s DefaultKind = %q, want %q", name, p.DefaultKind, kind)
		}
	}
}

func TestPresets_query_texts(t *testing.T) {
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
	for name, want := range map[string]string{"apim": apim, "otel": otel} {
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

// Every preset skips a broken line or element between good ones, counts it, and never puts its content in an error.
func TestParsers_skip_bad_lines_and_count(t *testing.T) {
	apimRow := `{"ApimSubscriptionId":"checkout-web","ApiId":"orders-api","Method":"GET","Host":"gateway.example","Path":"/orders/1","Calls":"2","LastSeen":"2026-11-18T09:12:44Z","TableName":"PrimaryResult"}`
	otelLine := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"checkout-web"}}]},"scopeSpans":[{"spans":[{"kind":3,"startTimeUnixNano":"1794993164000000000","attributes":[{"key":"http.request.method","value":{"stringValue":"GET"}},{"key":"url.full","value":{"stringValue":"https://orders.internal/v1/orders/1"}}]}]}]}]}`
	garbage := marker + ` not a record {"x":`
	cases := []struct {
		name, preset, input string
		records             int
		skipped             int64
	}{
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
		{"generic json array", "generic", `[{"caller":"a","method":"GET","path":"/x","lastSeen":"2026-11-18"}, "` + marker + `", {"caller":"b","method":{"x":1},"path":"/y","lastSeen":"2026-11-18"}, {"caller":"c","method":"GET","path":"/y","lastSeen":"2026-11-18"}]`, 2, 2},
		{"apim json lines", "apim", apimRow + "\n" + garbage + "\n" + apimRow + "\n", 2, 1},
		{"apim array", "apim", "[\n" + apimRow + ",\n42,\n\"" + marker + "\",\n" + strings.Replace(apimRow, `"Calls":"2"`, `"Calls":"`+marker+`"`, 1) + ",\n" + apimRow + "\n]", 2, 3},
		{"apim broken array keeps what came before", "apim", "[\n" + apimRow + ",\n" + garbage + "\n" + apimRow + "\n]", 1, 1},
		{"otel", "otel", otelLine + "\n" + garbage + "\n" + otelLine + "\n", 2, 1},
		{"otel json array", "otel", "[" + otelLine + ",\n42,\n" + otelLine + "]", 2, 1},
	}
	for _, c := range cases {
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
	}
	for preset, input := range inputs {
		p, _ := PresetByName(preset)
		_, err := p.Parse(&failingReader{strings.NewReader(input)}, ParseOptions{}, func(Record) {})
		if !errors.Is(err, errDisk) {
			t.Errorf("%s: err = %v, want the read error", preset, err)
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
			skipped, err := p.Parse(pr, ParseOptions{}, func(Record) {
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
			if err != nil || skipped != 0 || n != total {
				t.Errorf("records = %d, skipped = %d, err = %v; want %d, 0, nil", n, skipped, err, total)
			}
		})
	}
}
