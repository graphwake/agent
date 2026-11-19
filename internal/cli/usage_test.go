package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graphwake/agent/internal/usage"
)

// usageTemplates is the fake's GET /api/ingest/usage/templates answer: one service, orders, under /v1.
const usageTemplates = `{"source":{"id":"src-1","name":"gateway","preset":"generic"},"limits":{"maxCallers":2000,"maxRows":50000},
  "services":[{"id":"svc-orders","name":"orders","serverUrls":["https://orders.internal/v1"],"endpoints":[
    {"id":"ep-get","method":"GET","path":"/orders/{}"},{"id":"ep-list","method":"GET","path":"/orders"}]}]}`

// usageAnswer is the fake's POST /api/ingest/usage answer.
const usageAnswer = `{"callers":2,"newCallers":1,"rows":3,"skippedRows":0,"suggestions":2,"subscriptionsAdded":0}`

// usageCSV is generic input: 3 rows that match (2 callers), 5 requests that match no endpoint, 7 without a caller
// name, 3 from a personal-looking caller and 1 line that cannot be read.
const usageCSV = `caller,callerKind,service,method,path,count,lastSeen
checkout-web,client,orders,GET,/v1/orders/8812?token=abc,42,2026-11-18
checkout-web,client,orders,GET,/v1/orders,3,2026-11-17
billing-worker,service,orders,GET,/v1/orders/7,1,2026-11-16
checkout-web,client,orders,GET,/legacy/8812?token=abc,5,2026-11-15
,client,orders,GET,/v1/orders/1,7,2026-11-14
jane@contoso.com,client,orders,GET,/v1/orders/2,3,2026-11-13
not,enough
`

const usageSentLine = "graphwake: usage sent: 2 callers, 3 endpoint rows (1 new caller, 2 suggestions, 0 subscriptions added). " +
	"5 requests matched no endpoint. 7 requests had no caller name. " +
	"3 requests were withheld because the caller name looked personal. 1 line could not be read."

// usageRun runs "graphwake usage" (args after "usage") against the fakes and asserts exit code 0 and, unless
// multiLine, exactly one output line.
func (b *backend) usageRun(t *testing.T, r run, multiLine bool) string {
	t.Helper()
	s, code := b.main(t, "usage", r, nil)
	if code != 0 {
		t.Errorf("exit code %d, want 0 (out %q)", code, s)
	}
	if !multiLine && strings.Count(s, "\n") != 1 {
		t.Errorf("want exactly one output line, got %q", s)
	}
	if multiLine {
		return s
	}
	return strings.TrimSuffix(s, "\n")
}

func writeInput(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "usage.csv")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUsage_sends_snapshot_and_exits_zero(t *testing.T) {
	b := newBackend(t)
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, usageCSV)}}, false)
	if out != usageSentLine {
		t.Fatalf("out = %q\nwant  %q", out, usageSentLine)
	}
	if b.usageGets != 1 || len(b.usagePosts) != 1 {
		t.Fatalf("%d templates calls, %d posts", b.usageGets, len(b.usagePosts))
	}
	post := b.usagePosts[0]
	if post.Header.Get("Authorization") != "Bearer "+usageKey || post.Header.Get("Content-Type") != "application/json" ||
		post.Header.Get("User-Agent") != "graphwake-agent/1.0.0-test (other)" {
		t.Fatalf("headers %v", post.Header)
	}
	dec := json.NewDecoder(bytes.NewReader(post.Body))
	dec.DisallowUnknownFields()
	var snap usage.Snapshot
	if err := dec.Decode(&snap); err != nil {
		t.Fatalf("body is not a Snapshot: %v\n%s", err, post.Body)
	}
	if snap.Preset != "generic" || snap.AgentVersion != "1.0.0-test" || !snap.CountsIncluded ||
		snap.UnmatchedRequests != 5 || snap.AnonymousRequests != 7 || snap.WithheldRequests != 3 || len(snap.Rows) != 3 ||
		snap.WindowStart != "2026-11-13" || snap.WindowEnd != "2026-11-18" {
		t.Fatalf("snapshot %+v", snap)
	}
	if r := snap.Rows[2]; r.Caller != "checkout-web" || r.EndpointID != "ep-get" || r.Path != "/orders/{}" ||
		r.Count == nil || *r.Count != 42 || r.LastSeen != "2026-11-18" {
		t.Fatalf("row %+v", r)
	}
	if b.sent() != nil {
		t.Fatalf("spec upload called: %v", b.sent())
	}
}

func TestUsage_flags_from_environment(t *testing.T) {
	b := newBackend(t)
	out := b.usageRun(t, run{env: map[string]string{
		"GRAPHWAKE_USAGE_SOURCE": "generic", "GRAPHWAKE_USAGE_INPUT": writeInput(t, usageCSV), "GRAPHWAKE_USAGE_NO_COUNTS": "true",
	}}, false)
	if out != usageSentLine || len(b.usagePosts) != 1 {
		t.Fatalf("out = %q, %d posts", out, len(b.usagePosts))
	}
	var snap usage.Snapshot
	json.Unmarshal(b.usagePosts[0].Body, &snap)
	if snap.CountsIncluded || snap.Rows[0].Count != nil || bytes.Contains(b.usagePosts[0].Body, []byte(`"count"`)) {
		t.Fatalf("counts sent with --no-counts: %s", b.usagePosts[0].Body)
	}

	// --key on the command line instead of the environment; --no-counts=false overrides the environment.
	out = b.usageRun(t, run{env: map[string]string{"GRAPHWAKE_USAGE_KEY": "<unset>", "GRAPHWAKE_USAGE_NO_COUNTS": "1"},
		args: []string{"--key", usageKey, "--source", "generic", "--input", writeInput(t, usageCSV), "--no-counts=false"}}, false)
	if out != usageSentLine || len(b.usagePosts) != 2 || b.usagePosts[1].Header.Get("Authorization") != "Bearer "+usageKey {
		t.Fatalf("out = %q, %d posts", out, len(b.usagePosts))
	}
	json.Unmarshal(b.usagePosts[1].Body, &snap)
	if !snap.CountsIncluded {
		t.Fatal("--no-counts=false did not override GRAPHWAKE_USAGE_NO_COUNTS")
	}
}

func TestUsage_reads_stdin_when_no_input_flag(t *testing.T) {
	b := newBackend(t)
	out := b.usageRun(t, run{args: []string{"--source", "generic"}, stdin: strings.NewReader(usageCSV)}, false)
	if out != usageSentLine || len(b.usagePosts) != 1 {
		t.Fatalf("out = %q, %d posts", out, len(b.usagePosts))
	}

	for name, stdin := range map[string]run{
		"no stdin":    {args: []string{"--source", "generic"}},
		"empty stdin": {args: []string{"--source", "generic"}, stdin: strings.NewReader("")},
	} {
		if out := b.usageRun(t, stdin, false); out != "WARNING graphwake: usage not sent: no input (give --input or pipe a file)" {
			t.Errorf("%s: out = %q", name, out)
		}
	}
	if len(b.usagePosts) != 1 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

func TestUsage_dry_run_sends_nothing(t *testing.T) {
	b := newBackend(t)
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, usageCSV), "--dry-run"}}, true)
	if len(b.usagePosts) != 0 || b.usageGets != 1 {
		t.Fatalf("%d posts, %d templates calls", len(b.usagePosts), b.usageGets)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[0] != "--- would send ---" || lines[2] != "--- end ---" ||
		lines[len(lines)-1] != "  Requests without a caller name: 7" {
		t.Fatalf("out =\n%s", out)
	}
	var snap usage.Snapshot
	if err := json.Unmarshal([]byte(lines[1]), &snap); err != nil || len(snap.Rows) != 3 {
		t.Fatalf("would-send body %q: %v", lines[1], err)
	}
	// The local table shows the unmatched prefix; the body never does.
	if !strings.Contains(out, "\n    - (no host) /legacy/*  5\n") || strings.Contains(lines[1], "legacy") {
		t.Fatalf("out =\n%s", out)
	}

	// From the environment, and also when nothing matched (the table is what the Admin needs then).
	out = b.usageRun(t, run{env: map[string]string{"GRAPHWAKE_USAGE_DRY_RUN": "yes"}, args: []string{"--source", "generic"},
		stdin: strings.NewReader("caller,method,path,lastSeen\napp,GET,/nothing/here,2026-11-18\n")}, true)
	if !strings.HasPrefix(out, "--- would send ---\n") || !strings.Contains(out, "\n    - (no host) /nothing/here  1\n") ||
		len(b.usagePosts) != 0 {
		t.Fatalf("out =\n%s", out)
	}
}

func TestUsage_query_prints_kql_without_network(t *testing.T) {
	b := newBackend(t)
	apim, _ := usage.PresetByName("apim")
	if out := b.usageRun(t, run{args: []string{"query", "--source", "APIM"}}, true); out != apim.Query ||
		!strings.Contains(out, "ApiManagementGatewayLogs") {
		t.Fatalf("out =\n%s", out)
	}
	otel, _ := usage.PresetByName("otel")
	if out := b.usageRun(t, run{args: []string{"query"}, env: map[string]string{"GRAPHWAKE_USAGE_SOURCE": "otel"}}, true); out != otel.Query {
		t.Fatalf("out =\n%s", out)
	}
	if out := b.usageRun(t, run{args: []string{"query", "--source", "nope"}}, false); out !=
		`WARNING graphwake: unknown source "nope" (one of: apim, appinsights, datadog, generic, kong, nginx, otel)` {
		t.Fatalf("out = %q", out)
	}
	if b.usageGets != 0 || len(b.usagePosts) != 0 {
		t.Fatalf("network used: %d gets, %d posts", b.usageGets, len(b.usagePosts))
	}
}

func TestUsage_every_failure_exits_zero(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	input := writeInput(t, usageCSV)
	tests := []struct {
		name                        string
		env                         map[string]string
		args                        []string
		input                       string // "" = usageCSV
		templatesStatus, postStatus int
		templates, answer           string
		want                        string
	}{
		{name: "no key", env: map[string]string{"GRAPHWAKE_USAGE_KEY": "<unset>"},
			want: "usage not sent: no usage key (set GRAPHWAKE_USAGE_KEY)"},
		{name: "unknown source", args: []string{"--source", "acme-gateway"},
			want: `usage not sent: unknown source "acme-gateway" (one of: apim, appinsights, datadog, generic, kong, nginx, otel)`},
		{name: "no source", args: []string{"--source", ""},
			want: "usage not sent: no source given (one of: apim, appinsights, datadog, generic, kong, nginx, otel)"},
		{name: "unknown flag", args: []string{"--nope"}, want: "usage not sent: invalid arguments: unknown flag"},
		{name: "bad URL", env: map[string]string{"GRAPHWAKE_URL": "ftp://x"},
			want: "usage not sent: GRAPHWAKE_URL must be an http(s) URL"},
		{name: "input file missing", args: []string{"--input", "/nope/secret-name.csv"},
			want: "usage not sent: the input file could not be opened"},
		{name: "input file empty", args: []string{"--input", writeInput(t, "")}, want: "usage not sent: the input file is empty"},
		{name: "input unreadable", args: []string{"--input", writeInput(t, "nothing,useful\n")},
			want: "usage not sent: the input could not be read"},
		{name: "unknown service", args: []string{"--service", "billing"},
			want: "usage not sent: --service names no service in the endpoint list Graphwake sent"},
		{name: "401", templatesStatus: 401, templates: `{"code":"invalid_key","error":"x"}`,
			want: "usage not sent: the key was not accepted"},
		{name: "403", templatesStatus: 403, templates: `{"code":"usage_not_in_plan","error":"x"}`,
			want: "usage not sent: Usage Discovery is not in your plan"},
		{name: "413", postStatus: 413, answer: `{"code":"usage_snapshot_too_large","error":"x"}`,
			want: "usage not sent: the summary is too large"},
		{name: "422", postStatus: 422, answer: `{"code":"usage_too_many_callers","error":"x"}`,
			want: "usage not sent: too many distinct callers. The caller field may identify end users; choose another with --caller-field"},
		{name: "500", postStatus: 500, want: "usage not sent: unexpected response 500"},
		{name: "unreachable", env: map[string]string{"GRAPHWAKE_URL": closed.URL},
			want: "usage not sent: Graphwake could not be reached"},
		{name: "nothing matched", templates: `{"services":[]}`, want: "usage not sent: nothing in the input matched an endpoint"},
		{name: "all withheld", input: "caller,service,method,path,lastSeen\njane@contoso.com,orders,GET,/v1/orders/1,2026-11-18\n",
			want: "usage not sent: no caller name could be sent: every name was withheld (it matched a withholding rule: see the README) or was missing. Check --caller-field"},
		{name: "too many rows", templates: strings.Replace(usageTemplates, `"maxRows":50000`, `"maxRows":1`, 1),
			want: "usage not sent: the summary is too large"},
		{name: "all anonymous", input: "caller,service,method,path,lastSeen\n,orders,GET,/v1/orders/1,2026-11-18\n",
			want: "usage not sent: no caller names were found in the input. Check --caller-field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBackend(t)
			if tt.templatesStatus != 0 {
				b.templatesStatus = tt.templatesStatus
			}
			if tt.templates != "" {
				b.templates = tt.templates
			}
			if tt.postStatus != 0 {
				b.usageStatus, b.usageBody = tt.postStatus, tt.answer
			}
			in := input
			if tt.input != "" {
				in = writeInput(t, tt.input)
			}
			args := append([]string{"--source", "generic", "--input", in}, tt.args...)
			out := b.usageRun(t, run{env: tt.env, args: args}, false)
			if out != "WARNING graphwake: "+tt.want {
				t.Fatalf("out = %q\nwant  %q", out, "WARNING graphwake: "+tt.want)
			}
			if tt.postStatus == 0 && len(b.usagePosts) != 0 {
				t.Fatalf("%d posts", len(b.usagePosts))
			}
		})
	}
}

func TestUsage_output_never_contains_key_or_raw_path(t *testing.T) {
	b := newBackend(t)
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, usageCSV)}}, false)
	if out != usageSentLine || strings.Contains(out, "8812") || strings.Contains(out, "token") {
		t.Fatalf("out = %q", out)
	}
	if body := string(b.usagePosts[0].Body); strings.Contains(body, "8812") || strings.Contains(body, "token") ||
		strings.Contains(body, "jane") || strings.Contains(body, "legacy") {
		t.Fatalf("body leaks raw input: %s", body)
	}

	// A key pasted in the wrong place never comes back (the harness fails on any key in the output).
	for _, args := range [][]string{
		{"--source", usageKey},
		{"--key", usageKey, "--source", "generic", "--bogus=" + usageKey},
		{"--key=" + usageKey, "-" + usageKey},
		{"--source", "generic", usageKey},
	} {
		b.usageRun(t, run{env: map[string]string{"GRAPHWAKE_USAGE_KEY": "<unset>"}, args: args}, false)
	}
	long := "gwu_" + strings.Repeat("Ab3_", 10) + "xyz"
	if out := b.usageRun(t, run{args: []string{"--source", long}}, false); out !=
		"WARNING graphwake: usage not sent: unknown source (one of: apim, appinsights, datadog, generic, kong, nginx, otel)" {
		t.Fatalf("out = %q", out)
	}
}

func TestUsage_too_many_callers_is_not_sent(t *testing.T) {
	b := newBackend(t)
	b.templates = strings.Replace(usageTemplates, `"maxCallers":2000`, `"maxCallers":1`, 1)
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, usageCSV)}}, false)
	want := "WARNING graphwake: usage not sent: too many distinct callers (more than 1, limit 1). " +
		"The caller field may identify end users; choose another with --caller-field"
	if out != want || len(b.usagePosts) != 0 {
		t.Fatalf("out = %q, %d posts", out, len(b.usagePosts))
	}
	// --dry-run gives the same warning: there is no summary to show.
	if out := b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, usageCSV), "--dry-run"}}, false); out != want {
		t.Fatalf("dry run out = %q", out)
	}
}

func TestUsage_help(t *testing.T) {
	b := newBackend(t)
	out := b.usageRun(t, run{args: []string{"-h"}}, true)
	if !strings.HasPrefix(out, "usage: graphwake usage --source") || b.usageGets != 0 {
		t.Fatalf("out =\n%s", out)
	}
}

// The spec upload still declares its JSON body after the retry loop moved into ingest.Client.Do.
func TestUpload_still_sends_json_content_type(t *testing.T) {
	b := newBackend(t)
	b.run(t, run{args: []string{"--live"}})
	if len(b.contentTypes) != 1 || b.contentTypes[0] != "application/json" {
		t.Fatalf("content types %v", b.contentTypes)
	}
}

func TestUsage_success_line_plurals_and_rows_not_accepted(t *testing.T) {
	b := newBackend(t)
	b.usageBody = `{"callers":1,"newCallers":0,"rows":1,"skippedRows":1,"suggestions":0,"subscriptionsAdded":0}`
	input := "caller,service,method,path,lastSeen\napp,orders,GET,/v1/orders/1,2026-11-18\nbroken\napp,orders,GET\n"
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, input)}}, false)
	want := "graphwake: usage sent: 1 caller, 1 endpoint row (0 new callers, 0 suggestions, 0 subscriptions added). " +
		"2 lines could not be read. 1 row was not accepted."
	if out != want {
		t.Fatalf("out = %q\nwant  %q", out, want)
	}
	b.usageBody = `{"callers":2,"newCallers":1,"rows":3,"skippedRows":4,"suggestions":2,"subscriptionsAdded":0}`
	out = b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, usageCSV)}}, false)
	if out != usageSentLine+" 4 rows were not accepted." {
		t.Fatalf("out = %q", out)
	}
}

// --service pins records that name no service and have no known host; --caller-field picks another column.
func TestUsage_service_and_caller_field(t *testing.T) {
	b := newBackend(t)
	input := "app,method,path,lastSeen\ncheckout-web,GET,/v1/orders/9,2026-11-18\nbilling-worker,GET,/orders,2026-11-17\n"
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, input),
		"--service", "orders", "--caller-field", "app"}}, false)
	if out != "graphwake: usage sent: 2 callers, 3 endpoint rows (1 new caller, 2 suggestions, 0 subscriptions added)." ||
		len(b.usagePosts) != 1 {
		t.Fatalf("out = %q, %d posts", out, len(b.usagePosts))
	}
	var snap usage.Snapshot
	if err := json.Unmarshal(b.usagePosts[0].Body, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Rows) != 2 || snap.Rows[0].Caller != "billing-worker" || snap.Rows[0].EndpointID != "ep-list" ||
		snap.Rows[1].Caller != "checkout-web" || snap.Rows[1].EndpointID != "ep-get" || snap.Rows[1].Service != "orders" {
		t.Fatalf("rows %+v", snap.Rows)
	}
	// Without --service nothing in that input names a service: nothing matches.
	out = b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, input), "--caller-field", "app"}}, false)
	if out != "WARNING graphwake: usage not sent: nothing in the input matched an endpoint" {
		t.Fatalf("out = %q", out)
	}
}

// Raw paths with line breaks and escapes reach only the local table, escaped: no output line starts a CI command.
// (A "#" in a path starts the fragment, which the preset drops; "##vso[" in a host is covered in the usage package.)
func TestUsage_dry_run_output_is_escaped(t *testing.T) {
	b := newBackend(t)
	input := `{"caller":"app","method":"GET","path":"/x\n::error::y/z\r\u001b[31mw","lastSeen":"2026-11-18"}` + "\n"
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--dry-run"}, stdin: strings.NewReader(input)}, true)
	if strings.ContainsAny(out, "\r\x1b") || !strings.Contains(out, "\n    - (no host) /x\\x0a::error::y/*  1\n") {
		t.Fatalf("out = %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "::") || strings.Contains(line, "##") {
			t.Fatalf("line can start a CI command: %q", line)
		}
	}
	if len(b.usagePosts) != 0 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

// A caller name carrying a CI logging command is withheld: it is neither sent nor printed, only counted.
func TestUsage_caller_with_ci_command_is_withheld(t *testing.T) {
	b := newBackend(t)
	input := "caller,service,method,path,lastSeen\nx ##vso[task.logissue]y,orders,GET,/v1/orders/1,2026-11-18\n"
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--dry-run"}, stdin: strings.NewReader(input)}, true)
	if strings.Contains(out, "##vso") || strings.Contains(out, "logissue") ||
		!strings.Contains(out, `"withheldRequests":1`) || !strings.Contains(out, `"rows":[]`) ||
		!strings.Contains(out, "Requests withheld because the caller name looked personal: 1\n") {
		t.Fatalf("out =\n%s", out)
	}
	if len(b.usagePosts) != 0 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

// A Datadog answer that reports a timeout or a warning still sends what it holds; the success line says the export
// was incomplete. The alias "dd" names the datadog preset, and the snapshot carries the canonical name.
func TestUsage_reports_an_incomplete_export(t *testing.T) {
	bucket := func(service, path string, count int) string {
		return fmt.Sprintf(`{"type":"bucket","attributes":{"by":{"service":%q,"@http.url_details.host":"orders.internal",`+
			`"@http.url_details.path":%q,"@http.method":"GET"},"compute":{"c0":%d}}}`, service, path, count)
	}
	answer := func(status string) string {
		return `{"data":[` + bucket("checkout-web", "/v1/orders/8812", 42) + `,` + bucket("checkout-web", "/v1/orders", 3) +
			`],"meta":{"status":"` + status + `"}}` + `{"data":[` + bucket("billing-worker", "/v1/orders/7", 1) + `],"meta":{"status":"done"}}`
	}
	const sent = "graphwake: usage sent: 2 callers, 3 endpoint rows (1 new caller, 2 suggestions, 0 subscriptions added)."
	const incomplete = " The export was incomplete (the tool reported a timeout or a warning)."

	b := newBackend(t)
	out := b.usageRun(t, run{args: []string{"--source", "dd"}, stdin: strings.NewReader(answer("timeout"))}, false)
	if out != sent+incomplete || len(b.usagePosts) != 1 {
		t.Fatalf("out = %q, %d posts\nwant  %q", out, len(b.usagePosts), sent+incomplete)
	}
	var snap usage.Snapshot
	if err := json.Unmarshal(b.usagePosts[0].Body, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Preset != "datadog" || len(snap.Rows) != 3 {
		t.Fatalf("snapshot %+v", snap)
	}

	// The sentence comes after the others.
	out = b.usageRun(t, run{args: []string{"--source", "datadog"}, stdin: strings.NewReader(answer("timeout") + "\nnot json\n")}, false)
	if out != sent+" 1 line could not be read."+incomplete {
		t.Fatalf("out = %q", out)
	}

	// A complete export says nothing of the kind.
	out = b.usageRun(t, run{args: []string{"--source", "datadog"}, stdin: strings.NewReader(answer("done"))}, false)
	if out != sent {
		t.Fatalf("out = %q\nwant  %q", out, sent)
	}
}

// A partial export is never silent: an incomplete export without a single request is not sent and says why, and the
// dry run ends with a note.
func TestUsage_partial_export_is_never_silent(t *testing.T) {
	const bucket = `{"type":"bucket","attributes":{"by":{"service":"checkout-web","@http.url_details.host":"orders.internal",` +
		`"@http.url_details.path":"/v1/orders/8812","@http.method":"GET"},"compute":{"c0":42}}}`
	const note = "Note: the export was incomplete (the tool reported a timeout or a warning)."
	b := newBackend(t)
	for name, in := range map[string]string{
		"timeout":  `{"data":[],"meta":{"status":"timeout"}}`,
		"warnings": `{"data":[],"meta":{"status":"done","warnings":[{"code":"x"}]}}`,
		"dry run":  `{"data":[],"meta":{"status":"timeout"}}`,
	} {
		args := []string{"--source", "datadog"}
		if name == "dry run" {
			args = append(args, "--dry-run")
		}
		out := b.usageRun(t, run{args: args, stdin: strings.NewReader(in)}, false)
		if out != "WARNING graphwake: usage not sent: the export was incomplete (the tool reported a timeout or a warning) and held no requests" {
			t.Errorf("%s: out = %q", name, out)
		}
	}
	// A complete export that held no requests says so; a truly empty input keeps its own message.
	for name, c := range map[string]struct {
		args        []string
		stdin, want string
	}{
		"no buckets":           {[]string{"--source", "datadog"}, `{"data":[],"meta":{"status":"done"}}`, "usage not sent: the input held no requests"},
		"csv header only":      {[]string{"--source", "generic"}, "caller,method,path,lastSeen\n", "usage not sent: the input held no requests"},
		"no buckets in file":   {[]string{"--source", "datadog", "--input", writeInput(t, `{"data":[]}`)}, "", "usage not sent: the input held no requests"},
		"blank stdin":          {[]string{"--source", "datadog"}, " \n\r\n", "usage not sent: " + noInput},
		"byte order mark only": {[]string{"--source", "datadog", "--input", writeInput(t, "\xef\xbb\xbf\n")}, "", "usage not sent: the input file is empty"},
	} {
		r := run{args: c.args}
		if c.stdin != "" {
			r.stdin = strings.NewReader(c.stdin)
		}
		if out := b.usageRun(t, r, false); out != "WARNING graphwake: "+c.want {
			t.Errorf("%s: out = %q, want %q", name, out, c.want)
		}
	}

	out := b.usageRun(t, run{args: []string{"--source", "datadog", "--dry-run"},
		stdin: strings.NewReader(`{"data":[` + bucket + `],"meta":{"status":"timeout"}}`)}, true)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[0] != "--- would send ---" || lines[len(lines)-1] != note || strings.Count(out, note) != 1 {
		t.Fatalf("out =\n%s", out)
	}
	out = b.usageRun(t, run{args: []string{"--source", "datadog", "--dry-run"},
		stdin: strings.NewReader(`{"data":[` + bucket + `],"meta":{"status":"done"}}`)}, true)
	if strings.Contains(out, "Note:") {
		t.Fatalf("complete export: out =\n%s", out)
	}
	if len(b.usagePosts) != 0 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

// A caller field that names a path (here Kong's request.uri) gives path-like caller names: they are withheld, so
// neither the path nor its query string reaches the summary or the output.
func TestUsage_path_as_caller_is_withheld(t *testing.T) {
	b := newBackend(t)
	input := `{"consumer":{"username":"checkout-web"},"request":{"method":"GET","uri":"/v1/orders/8812?token=abc",` +
		`"headers":{"host":"orders.internal"}},"started_at":1794993164123}` + "\n" +
		`{"consumer":{"username":"checkout-web"},"request":{"method":"GET","uri":"/v1/orders?page=2",` +
		`"headers":{"host":"orders.internal"}},"started_at":1794993164123}` + "\n"
	out := b.usageRun(t, run{args: []string{"--source", "kong", "--caller-field", "request.uri", "--dry-run"},
		stdin: strings.NewReader(input)}, true)
	if !strings.Contains(out, `"withheldRequests":2`) || !strings.Contains(out, `"rows":[]`) ||
		!strings.Contains(out, "Requests withheld because the caller name looked personal: 2\n") {
		t.Fatalf("out =\n%s", out)
	}
	for _, leak := range []string{"8812", "token", "page=2", "/v1/orders"} {
		if strings.Contains(out, leak) {
			t.Fatalf("output contains %q:\n%s", leak, out)
		}
	}
	if len(b.usagePosts) != 0 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

// A dry run whose summary has no rows says that the real run would send nothing; one with rows does not.
func TestUsage_dry_run_says_when_nothing_would_be_sent(t *testing.T) {
	const note = "Note: this summary has no rows, so it would not be sent."
	b := newBackend(t)
	out := b.usageRun(t, run{args: []string{"--source", "generic", "--dry-run"},
		stdin: strings.NewReader("caller,method,path,lastSeen\napp,GET,/nothing/here,2026-11-18\n")}, true)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[len(lines)-1] != note || strings.Count(out, note) != 1 {
		t.Fatalf("out =\n%s", out)
	}
	// The real run with the same input sends nothing.
	if out := b.usageRun(t, run{args: []string{"--source", "generic"},
		stdin: strings.NewReader("caller,method,path,lastSeen\napp,GET,/nothing/here,2026-11-18\n")}, false); out !=
		"WARNING graphwake: usage not sent: nothing in the input matched an endpoint" {
		t.Fatalf("out = %q", out)
	}
	out = b.usageRun(t, run{args: []string{"--source", "generic", "--input", writeInput(t, usageCSV), "--dry-run"}}, true)
	if strings.Contains(out, "Note:") {
		t.Fatalf("summary with rows: out =\n%s", out)
	}
	if len(b.usagePosts) != 0 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

// A caller field that names a method-prefixed path (Datadog's resource_name, "GET /v1/orders/8812") is withheld:
// neither the path nor its id reaches the summary or the output.
func TestUsage_method_prefixed_path_as_caller_is_withheld(t *testing.T) {
	b := newBackend(t)
	input := `{"data":[{"type":"bucket","attributes":{"by":{"service":"checkout-web","resource_name":"GET /v1/orders/8812",` +
		`"@http.url_details.host":"orders.internal","@http.url_details.path":"/v1/orders/8812","@http.method":"GET"},` +
		`"compute":{"c0":42}}}],"meta":{"status":"done"}}`
	out := b.usageRun(t, run{args: []string{"--source", "datadog", "--caller-field", "resource_name", "--dry-run"},
		stdin: strings.NewReader(input)}, true)
	if !strings.Contains(out, `"withheldRequests":42`) || !strings.Contains(out, `"rows":[]`) {
		t.Fatalf("out =\n%s", out)
	}
	for _, leak := range []string{"GET /v1", "/v1/orders", "8812"} {
		if strings.Contains(out, leak) {
			t.Fatalf("output contains %q:\n%s", leak, out)
		}
	}
	if len(b.usagePosts) != 0 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

// A caller field that names a request body (here an NGINX log member request_body) is withheld: the body text never
// reaches the summary or the output.
func TestUsage_body_as_caller_is_withheld(t *testing.T) {
	b := newBackend(t)
	input := `{"time":"2026-11-18T10:00:00+00:00","method":"GET","host":"orders.internal","path":"/v1/orders/8812",` +
		`"caller":"checkout-web","request_body":"{\"card\":\"4111111111111111\",\"cvc\":\"123\"}"}` + "\n"
	out := b.usageRun(t, run{args: []string{"--source", "nginx", "--caller-field", "request_body", "--dry-run"},
		stdin: strings.NewReader(input)}, true)
	if !strings.Contains(out, `"withheldRequests":1`) || !strings.Contains(out, `"rows":[]`) {
		t.Fatalf("out =\n%s", out)
	}
	for _, leak := range []string{"card", "4111", "cvc"} {
		if strings.Contains(out, leak) {
			t.Fatalf("output contains %q:\n%s", leak, out)
		}
	}
	if len(b.usagePosts) != 0 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

// A certificate subject as the caller ($ssl_client_s_dn, mutual TLS) is reduced to its common name before the
// withholding rules: the common name is sent, the other parts of the subject are not.
func TestUsage_certificate_subject_as_caller_sends_the_common_name(t *testing.T) {
	b := newBackend(t)
	line := func(subject string) string {
		return `{"time":"2026-11-18T10:00:00+00:00","method":"GET","host":"orders.internal","path":"/v1/orders/8812",` +
			`"caller":"","ssl_client_s_dn":"` + subject + `"}` + "\n"
	}
	input := line("CN=checkout,OU=Payments,O=Acme,C=NL") + // RFC 2253 form
		line("/C=NL/O=Acme/OU=Payments/CN=checkout") + // legacy form
		line("CN=jane@example.com,O=Acme") + // the common name goes through the rules: an e-mail address
		line("OU=Payments,O=Acme") // no common name: left as it is, and withheld
	out := b.usageRun(t, run{args: []string{"--source", "nginx", "--caller-field", "ssl_client_s_dn", "--dry-run"},
		stdin: strings.NewReader(input)}, true)
	if !strings.Contains(out, `"caller":"checkout"`) || !strings.Contains(out, `"count":2`) ||
		!strings.Contains(out, `"withheldRequests":2`) {
		t.Fatalf("out =\n%s", out)
	}
	for _, leak := range []string{"Acme", "Payments", "NL", "jane", "CN=", "O="} {
		if strings.Contains(out, leak) {
			t.Fatalf("output contains %q:\n%s", leak, out)
		}
	}
}

// A subject with a part that may name a person (a user DN with DC parts, a personal certificate with an e-mail
// address or a UID) is not reduced to its common name: it stays as it is and its "=" withholds it, so the person's
// name in its CN is never sent or printed.
func TestUsage_personal_subject_is_withheld_whole(t *testing.T) {
	b := newBackend(t)
	line := func(subject string) string {
		return `{"time":"2026-11-18T10:00:00+00:00","method":"GET","host":"orders.internal","path":"/v1/orders/8812",` +
			`"caller":"","ssl_client_s_dn":"` + subject + `"}` + "\n"
	}
	input := line("cn=John Smith,ou=people,dc=example,dc=com") +
		line("/C=US/O=Acme/CN=John Smith/emailAddress=j@x.com") +
		line("UID=jsmith,CN=John Smith") +
		line("CN=Jane Doe,emailAddress=jane@contoso.com")
	out := b.usageRun(t, run{args: []string{"--source", "nginx", "--caller-field", "ssl_client_s_dn", "--dry-run"},
		stdin: strings.NewReader(input)}, true)
	if !strings.Contains(out, `"withheldRequests":4`) || !strings.Contains(out, `"rows":[]`) {
		t.Fatalf("out =\n%s", out)
	}
	for _, leak := range []string{"John", "Smith", "Jane", "jsmith", "contoso"} {
		if strings.Contains(out, leak) {
			t.Fatalf("output contains %q:\n%s", leak, out)
		}
	}
}

// A redirect on the summary's POST is not followed: the summary never reaches the address it points to (here the
// fake backend, which counts every usage POST), and the run warns and exits 0.
func TestUsage_summary_never_follows_a_redirect(t *testing.T) {
	b := newBackend(t)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, usageTemplates)
			return
		}
		http.Redirect(w, r, b.srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(front.Close)
	out := b.usageRun(t, run{env: map[string]string{"GRAPHWAKE_URL": front.URL},
		args: []string{"--source", "generic", "--input", writeInput(t, usageCSV)}}, false)
	if out != "WARNING graphwake: usage not sent: unexpected response 307" {
		t.Fatalf("out = %q", out)
	}
	if b.usageGets != 0 || len(b.usagePosts) != 0 {
		t.Fatalf("the redirect target received %d templates calls and %d posts", b.usageGets, len(b.usagePosts))
	}
}

// The dry run's unmatched table goes to the job log: a host from the input cannot start a GitHub workflow command
// (the runner trims leading white space before it looks for "::"), segments that may be ids, addresses or tokens are
// masked, and the table stops after 20 rows.
func TestUsage_dry_run_table_is_safe_for_the_job_log(t *testing.T) {
	b := newBackend(t)
	entry := func(host, uri string) string {
		return `{"consumer":{"username":"checkout-web"},"request":{"method":"GET","uri":"` + uri + `",` +
			`"headers":{"host":"` + host + `"}},"started_at":1794993164123}` + "\n"
	}
	input := entry("::error::x", "/reset-password/eyJhbGciOiJIUzI1NiJ9.SECRETTOKEN") +
		entry("::warning::pwn", "/users/jane@contoso.com")
	for i := 0; i < 30; i++ {
		input += entry(fmt.Sprintf("host-%c.example", 'a'+i), "/x")
	}
	out := b.usageRun(t, run{args: []string{"--source", "kong", "--dry-run"}, stdin: strings.NewReader(input)}, true)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "::") {
			t.Errorf("line can start a workflow command: %q", line)
		}
	}
	for _, leak := range []string{"jane", "contoso", "SECRETTOKEN", "eyJ"} {
		if strings.Contains(out, leak) {
			t.Errorf("output contains %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "\n    - ::error::x /reset-password/*  1\n") || !strings.Contains(out, "\n    - and 12 more\n") ||
		strings.Count(out, "\n    - ") != 21 {
		t.Fatalf("out =\n%s", out)
	}
}

// panicReader stands for a bug: reading it panics.
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("bug with " + usageKey) }

// A bug in a usage command never fails the pipeline or prints what panicked: one fixed warning, exit code 0.
func TestUsage_internal_error_is_one_warning_and_exit_zero(t *testing.T) {
	b := newBackend(t)
	for _, args := range [][]string{{"--source", "generic"}, {"--source", "generic", "--dry-run"}} {
		out, code := b.main(t, "usage", run{args: args, stdin: panicReader{}}, nil)
		if code != 0 || out != "WARNING graphwake: usage not sent: internal error\n" {
			t.Fatalf("%v: code %d, out %q", args, code, out)
		}
	}
	if len(b.usagePosts) != 0 {
		t.Fatalf("%d posts", len(b.usagePosts))
	}
}

// "graphwake usage query" is covered by the same recovery: a bug there is one fixed warning and exit code 0.
func TestUsage_query_internal_error_is_one_warning_and_exit_zero(t *testing.T) {
	var out bytes.Buffer
	d := Deps{Out: &out, Getenv: func(string) string { panic("bug with " + usageKey) }}
	if code := usageCommand([]string{"query", "--source", "nginx"}, d); code != 0 ||
		out.String() != "WARNING graphwake: usage not sent: internal error\n" {
		t.Fatalf("code %d, out %q", code, out.String())
	}
}
