package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/graphwake/agent/internal/usage"
)

// The README is what customers' engineers and security reviewers read, so its Usage Discovery tables are checked
// against the code: the flags and variables against usage.go, the sources against usage.PresetNames, the field list
// against the Snapshot and Row types, and the versions it pins against the GitHub Action's.

const readmePath = "../../README.md"

func readFileT(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// readmeSection returns the text under the heading line (exact), up to the next heading of the same or a higher
// level.
func readmeSection(t *testing.T, readme, heading string) string {
	t.Helper()
	level := strings.Index(heading, " ")
	lines := strings.Split(readme, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("README has no heading %q", heading)
	}
	end, fenced := len(lines), false
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "```") {
			fenced = !fenced
			continue
		}
		if n := strings.Index(lines[i], " "); !fenced && n > 0 && n <= level && strings.Trim(lines[i][:n], "#") == "" {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// tableColumn returns the cells of column col of every Markdown table row in text whose header's first cell is
// header (header and separator rows excluded).
func tableColumn(text, header string, col int) []string {
	var cells []string
	inTable := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			inTable = false
			continue
		}
		parts := strings.Split(strings.Trim(line, "|"), "|")
		first := strings.TrimSpace(parts[0])
		switch {
		case first == header:
			inTable = true
		case !inTable || strings.HasPrefix(first, "---"):
		case col < len(parts):
			cells = append(cells, strings.TrimSpace(parts[col]))
		}
	}
	return cells
}

var codeSpan = regexp.MustCompile("`([^`]+)`")

// codeSpans returns the text of every `code span` in s.
func codeSpans(s string) []string {
	var spans []string
	for _, m := range codeSpan.FindAllStringSubmatch(s, -1) {
		spans = append(spans, m[1])
	}
	return spans
}

// usageFlagNames reads the names of the flags parseUsage defines (fs.StringVar and fs.BoolVar calls) from usage.go.
func usageFlagNames(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "usage.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "parseUsage" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasSuffix(sel.Sel.Name, "Var") {
				return true
			}
			if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				name, _ := strconv.Unquote(lit.Value)
				names = append(names, "--"+name)
			}
			return true
		})
		return false
	})
	if len(names) == 0 {
		t.Fatal("no flags found in parseUsage")
	}
	sort.Strings(names)
	return names
}

// usageVariableNames runs parseUsage with no flags and records the GRAPHWAKE_ variables it reads.
func usageVariableNames(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	if _, err := parseUsage(nil, func(name string) string {
		if strings.HasPrefix(name, "GRAPHWAKE_") {
			seen[name] = true
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestReadme_usage_flags_table_matches_the_code(t *testing.T) {
	section := readmeSection(t, readFileT(t, readmePath), "### Flags and environment")
	flagCells := tableColumn(section, "Flag", 0)
	varCells := tableColumn(section, "Flag", 1)
	if len(flagCells) == 0 {
		t.Fatal(`no table with the header "Flag" under "### Flags and environment"`)
	}
	documented := map[string]bool{}
	for _, cell := range flagCells {
		for _, span := range codeSpans(cell) {
			documented[span] = true
		}
	}
	want := usageFlagNames(t)
	for _, flag := range want {
		if !documented[flag] {
			t.Errorf("flag %s is not in the README's usage flags table", flag)
		}
	}
	// No cell of the table may name a flag graphwake usage does not define.
	for col := 0; col < 3; col++ {
		for _, cell := range tableColumn(section, "Flag", col) {
			for _, span := range codeSpans(cell) {
				if flag, _, _ := strings.Cut(span, " "); strings.HasPrefix(flag, "--") && !contains(want, flag) {
					t.Errorf("the README's usage flags table names %s, which graphwake usage does not define", flag)
				}
			}
		}
	}
	// Every variable the code reads is on the row of the flag it stands for, and the table names no other variable.
	pairs := usageFlagVariables(t, want)
	listed := map[string]string{} // variable -> the flag on its row
	for row, cell := range varCells {
		flags := codeSpans(flagCells[row])
		for _, span := range codeSpans(cell) {
			name, _, _ := strings.Cut(span, "=")
			if !strings.HasPrefix(name, "GRAPHWAKE_") {
				continue
			}
			if len(flags) != 1 {
				t.Errorf("row of %s names %d flags, want 1", name, len(flags))
				continue
			}
			listed[name] = flags[0]
		}
	}
	read := usageVariableNames(t)
	for _, name := range read {
		flag, ok := listed[name]
		switch {
		case !ok:
			t.Errorf("variable %s is not in the README's usage flags table", name)
		case pairs[flag] != name:
			t.Errorf("the README's usage flags table puts %s on the row of %s; the code reads it for %s", name, flag,
				flagFor(pairs, name))
		}
	}
	for name := range listed {
		if !contains(read, name) {
			t.Errorf("the README's usage flags table names %s, which graphwake usage does not read", name)
		}
	}
}

// usageFlagVariables pairs each flag with the variable that sets the same configuration field, found by running
// parseUsage once with only the flag and once with only each variable, and comparing which field changed.
func usageFlagVariables(t *testing.T, flags []string) map[string]string {
	t.Helper()
	none := func(string) string { return "" }
	base, err := parseUsage(nil, none)
	if err != nil {
		t.Fatal(err)
	}
	changed := func(c usageConfig) string {
		b, v := reflect.ValueOf(base), reflect.ValueOf(c)
		field := ""
		for i := 0; i < v.NumField(); i++ {
			if !reflect.DeepEqual(b.Field(i).Interface(), v.Field(i).Interface()) {
				if field != "" {
					t.Fatalf("more than one field changed: %s and %s", field, v.Type().Field(i).Name)
				}
				field = v.Type().Field(i).Name
			}
		}
		return field
	}
	byField := map[string]string{} // configuration field -> variable
	for _, name := range usageVariableNames(t) {
		c, err := parseUsage(nil, func(n string) string {
			if n == name {
				return "true" // a valid value for every variable: a switch, a name or a URL check happens later
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		if f := changed(c); f != "" {
			byField[f] = name
		}
	}
	pairs := map[string]string{}
	for _, flag := range flags {
		c, err := parseUsage([]string{flag + "=true"}, none)
		if err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		pairs[flag] = byField[changed(c)]
	}
	return pairs
}

func flagFor(pairs map[string]string, variable string) string {
	for flag, v := range pairs {
		if v == variable {
			return flag
		}
	}
	return "no flag"
}

// withheldProbes are names that trip the rules of usage.LooksPersonal, at least one for each rule (a control character
// cannot be written in a code span). Some trip more than one rule; the rows that pin each rule on its own are in
// usage.TestLooksPersonal. The README's withheld-name list must show each as an example; a new guard rule needs a
// probe here.
var withheldProbes = []string{
	"jane@contoso.com",   // e-mail address
	"10.0.0.12",          // IPv4
	"10.0.0.12:8080",     // IPv4 with a port
	"10.0.0.12.nip.io",   // IPv4 inside a host name
	"src:10.0.0.12",      // IPv4 behind a label
	"app 1.2.3.4",        // four numbers that are a version
	"2001:db8::1",        // IPv6
	"[fe80::1]:443",      // IPv6 in brackets with a port
	"client 2001:db8::1", // IPv6 as one piece of a name
	"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",    // secret-like
	"token-da39a3ee5e6b4b0d3255bfef95601890afd80709", // 32+ hexadecimal characters
	"x ##y",                    // "##"
	"/v1/orders/8812",          // starts with "/"
	"https://orders.example/a", // contains "://"
	"orders?page",              // contains "?"
	"GET /v1/orders/8812",      // a word that starts with "/"
	`{"card":"4111"}`,          // structured content: { } "
	"page=2&token=abc",         // structured content: =
	`<order id="1">`,           // structured content: < >
}

// notWithheldExamples are the names the README's list shows as not withheld or not caught; the guard must agree.
var notWithheldExamples = []string{"550e8400-e29b-41d4-a716-446655440000", "team/app", "Checkout / Web", "R&D team",
	"jane@localhost", "jan\u00e9@contoso.com", "ip10.0.0.12", "010.0.0.12", "ip-10-0-0-12", "src:2001:db8::1",
	"2001:db8::1.nip.io", "tok_abcdefghijklmnopqrstuvwxyz0123456789ab", "v1/orders/8812", "uri:/v1/x",
	"card:4111,cvc:123"}

// sentExamples are the values the README's safety-net paragraph names as sent; the guard must not withhold them.
var sentExamples = []string{"Jane Doe", "hunter2"}

func TestReadme_withheld_name_list_has_every_rule(t *testing.T) {
	section := readmeSection(t, readFileT(t, readmePath), "### What the usage step sends")
	start := strings.Index(section, "**Withheld caller names.**")
	if start < 0 {
		t.Fatal(`no "**Withheld caller names.**" paragraph under "### What the usage step sends"`)
	}
	list := section[start:]
	if end := strings.Index(list, "\n\n**"); end >= 0 {
		list = list[:end]
	}
	spans := codeSpans(list)
	for _, probe := range withheldProbes {
		if !usage.LooksPersonal(probe) {
			t.Errorf("probe %q is not withheld by the guard", probe)
		}
		if !contains(spans, probe) {
			t.Errorf("the README's withheld-name list has no example %q", probe)
		}
	}
	for _, name := range notWithheldExamples {
		if usage.LooksPersonal(name) {
			t.Errorf("%q is withheld by the guard, but the README's list shows it as not withheld", name)
		}
		if !contains(spans, name) {
			t.Errorf("the README's withheld-name list has no not-withheld example %q", name)
		}
	}
	net := section[start+len(list):]
	if !strings.HasPrefix(net, "\n\n**A safety net, not a guarantee.**") {
		t.Fatal(`no "**A safety net, not a guarantee.**" paragraph right after the withheld-name list`)
	}
	net = net[2:]
	if end := strings.Index(net, "\n\n"); end >= 0 {
		net = net[:end]
	}
	for _, name := range sentExamples {
		if usage.LooksPersonal(name) {
			t.Errorf("%q is withheld by the guard, but the README says it is sent", name)
		}
		if !contains(codeSpans(net), name) {
			t.Errorf("the README's safety-net paragraph has no example %q", name)
		}
	}
	// Every other full example in the list is withheld (the list also quotes characters, such as "+/_=-", and the
	// field withheldRequests, which are not names).
	for _, span := range spans {
		if len(span) > 8 && span != "withheldRequests" && !contains(notWithheldExamples, span) && !usage.LooksPersonal(span) {
			t.Errorf("the README's withheld-name list shows %q, which the guard does not withhold", span)
		}
	}
}

var (
	helpFlag     = regexp.MustCompile(`--[a-z][a-z-]*[a-z]`)
	helpVariable = regexp.MustCompile(`GRAPHWAKE_[A-Z_]*[A-Z]`)
)

// The help text "graphwake usage -h" prints names the same flags and variables as the code, like the README table.
func TestUsageHelp_names_the_flags_and_variables_of_the_code(t *testing.T) {
	flags := usageFlagNames(t)
	helpFlags := helpFlag.FindAllString(usageHelp, -1)
	for _, flag := range flags {
		if !contains(helpFlags, flag) {
			t.Errorf("the usage help text does not name %s", flag)
		}
	}
	for _, flag := range helpFlags {
		if !contains(flags, flag) {
			t.Errorf("the usage help text names %s, which graphwake usage does not define", flag)
		}
	}
	variables := usageVariableNames(t)
	helpVariables := helpVariable.FindAllString(usageHelp, -1)
	for _, name := range variables {
		if !contains(helpVariables, name) {
			t.Errorf("the usage help text does not name %s", name)
		}
	}
	for _, name := range helpVariables {
		if !contains(variables, name) {
			t.Errorf("the usage help text names %s, which graphwake usage does not read", name)
		}
	}
}

func TestReadme_usage_sources_table_lists_every_preset(t *testing.T) {
	section := readmeSection(t, readFileT(t, readmePath), "### Supported sources")
	cells := tableColumn(section, "Source", 0)
	if len(cells) == 0 {
		t.Fatal(`no table with the header "Source" under "### Supported sources"`)
	}
	var listed []string
	for _, cell := range cells {
		listed = append(listed, codeSpans(cell)...)
	}
	sort.Strings(listed)
	if want := usage.PresetNames(); !reflect.DeepEqual(listed, want) {
		t.Errorf("sources table lists %v, want %v", listed, want)
	}
}

// jsonFieldNames returns the JSON member names of a struct's exported, tagged fields.
func jsonFieldNames(typ reflect.Type) []string {
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		tag, ok := typ.Field(i).Tag.Lookup("json")
		if !ok || tag == "-" {
			continue
		}
		names = append(names, strings.Split(tag, ",")[0])
	}
	return names
}

func TestReadme_usage_field_list_is_the_snapshot(t *testing.T) {
	section := readmeSection(t, readFileT(t, readmePath), "### What the usage step sends")
	cells := tableColumn(section, "Field", 0)
	if len(cells) == 0 {
		t.Fatal(`no table with the header "Field" under "### What the usage step sends"`)
	}
	var listed []string
	for _, cell := range cells {
		listed = append(listed, codeSpans(cell)...)
	}
	sort.Strings(listed)
	want := append(jsonFieldNames(reflect.TypeOf(usage.Snapshot{})), jsonFieldNames(reflect.TypeOf(usage.Row{}))...)
	sort.Strings(want)
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("README field list = %v\nwant (Snapshot and Row) %v", listed, want)
	}
}

var (
	actionVersion = regexp.MustCompile(`(?m)^  version:\n(?:    .*\n)*?    default: "([^"]+)"`)
	readmePins    = []*regexp.Regexp{
		regexp.MustCompile(`github-action@v([0-9][0-9A-Za-z.\-]*)`),
		regexp.MustCompile(`ghcr\.io/graphwake/agent:([0-9][0-9A-Za-z.\-]*)`),
		regexp.MustCompile(`VERSION=([0-9][0-9A-Za-z.\-]*)`),
		regexp.MustCompile(`graphwake/agent/v([0-9][0-9A-Za-z.\-]*)/gitlab`),
	}
)

func TestReadme_pinned_versions_match_the_github_action(t *testing.T) {
	m := actionVersion.FindStringSubmatch(readFileT(t, "../../github-action/action.yml"))
	if m == nil {
		t.Fatal("no version input default in github-action/action.yml")
	}
	want := strings.TrimSuffix(m[1], ".")
	readme := readFileT(t, readmePath)
	found := 0
	for _, re := range readmePins {
		for _, pin := range re.FindAllStringSubmatch(readme, -1) {
			found++
			if got := strings.TrimSuffix(pin[1], "."); got != want {
				t.Errorf("README pins %q (%s), the GitHub Action %s", got, pin[0], want)
			}
		}
	}
	if found == 0 {
		t.Fatal("no pinned version found in the README")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
