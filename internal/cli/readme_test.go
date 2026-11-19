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
	variables := strings.Join(varCells, " ")
	for _, name := range usageVariableNames(t) {
		if !strings.Contains(variables, "`"+name+"`") && !strings.Contains(variables, "`"+name+"=") {
			t.Errorf("variable %s is not in the README's usage flags table", name)
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
