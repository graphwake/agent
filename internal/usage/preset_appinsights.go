package usage

import (
	"encoding/json"
	"io"
	"strings"
)

// appInsightsQuery is the Application Insights query whose result the appinsights preset reads.
const appInsightsQuery = `// Azure Application Insights (workspace-based): outgoing HTTP calls of your services, last 7 days.
// Run with:  az monitor log-analytics query --workspace <workspace-id> --analytics-query "<this query>" -o json
AppDependencies
| where TimeGenerated > ago(7d) and DependencyType startswith "http"
| extend u = parse_url(Data)
| extend Host = tolower(iff(isempty(tostring(u.Host)), Target, tostring(u.Host))),
         Path = strcat("/", trim_start(@"/", tostring(u.Path))),
         Method = extract(@"^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s", 1, Name)
| summarize Calls = sum(ItemCount), LastSeen = max(TimeGenerated) by AppRoleName, Host, Method, Path
`

// appInsightsPreset reads the result of appInsightsQuery (or of a similar query) in either shape the Azure CLI writes:
//   - `az monitor log-analytics query -o json`, the command appInsightsQuery names: an array of row objects (or one
//     object per line) whose values are all strings, with null written as "None";
//   - `az monitor app-insights query` (classic names such as cloud_RoleName): the API answer
//     {"tables":[{"name":…,"columns":[{"name":…}],"rows":[[…]]}]}, whose values keep their JSON types.
//
// Every row is a group of calls of the service the caller column names.
var appInsightsPreset = Preset{Name: "appinsights", DefaultKind: "service", Parse: parseAppInsights, Query: appInsightsQuery}

// The caller columns: AppRoleName in workspace-based Application Insights, cloud_RoleName in classic.
const (
	appInsightsCaller        = "AppRoleName"
	appInsightsClassicCaller = "cloud_RoleName"
)

func parseAppInsights(r io.Reader, opts ParseOptions, emit func(Record)) (ParseResult, error) {
	br, first, ok, err := openInput(r)
	if err != nil || !ok {
		return finish("appinsights", 0, 0, err)
	}
	row := func(get func(name string) (string, bool)) bool {
		rec, ok := appInsightsRecord(get, opts.CallerField)
		if ok {
			emit(rec)
		}
		return ok
	}
	var (
		read, skipped int64
		partial       bool
	)
	if first == '[' {
		// Log Analytics rows: an array of objects, read as the apim preset reads them.
		read, skipped, err = eachJSONValue(br, first, func(raw []byte) bool {
			o, ok := decodeObject(raw)
			return ok && row(o.get)
		})
	} else {
		read, skipped, err = eachJSONObject(br, first, func(s *jsonStream, count func(bool)) error {
			return appInsightsObject(s, count, opts.CallerField, &partial, row)
		})
	}
	res, err := finish("appinsights", read, skipped, err)
	res.Partial = partial
	return res, err
}

// appInsightsRow reads one row through get, emits its record and reports whether it could.
type appInsightsRow func(get func(name string) (string, bool)) bool

// appInsightsObject reads one top-level object: an API answer with "tables", whose rows are streamed, else one Log
// Analytics row. Of a row object only the query's columns (and the --caller-field column) are kept. An "error" member
// that is not null (the API reports partial results, such as a truncated answer, that way) sets partial; nothing of it
// is kept.
func appInsightsObject(s *jsonStream, count func(bool), callerField string, partial *bool, row appInsightsRow) error {
	if _, err := s.Token(); err != nil { // '{'
		return err
	}
	fields, answer := jsonObject{}, false
	for s.More() {
		key, err := s.Key()
		if err != nil {
			return err
		}
		switch {
		case strings.EqualFold(key, "tables"):
			answer = true
			err = appInsightsTables(s, count, row)
		case strings.EqualFold(key, "error"):
			var t json.Token
			if t, err = s.Token(); err == nil {
				if t != nil {
					*partial = true
				}
				err = s.Skip(t)
			}
		case appInsightsColumn(key, callerField):
			var raw json.RawMessage
			if raw, err = s.Value(); err == nil {
				if _, dup := fields[key]; !dup {
					fields[key] = raw
				}
			}
		default:
			err = s.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	if _, err := s.Token(); err != nil { // '}'
		return err
	}
	if !answer {
		count(row(fields.get))
	}
	return nil
}

// appInsightsColumn reports whether a member of a row object is one the preset reads.
func appInsightsColumn(name, callerField string) bool {
	for _, c := range []string{callerField, appInsightsCaller, appInsightsClassicCaller, "Host", "Method", "Path", "Calls", "LastSeen"} {
		if c != "" && strings.EqualFold(name, c) {
			return true
		}
	}
	return false
}

// appInsightsTables reads the "tables" array of an API answer. Every table is read the same way: a table without the
// query's columns gives rows without a method, which are skipped and counted.
func appInsightsTables(s *jsonStream, count func(bool), row appInsightsRow) error {
	t, err := s.Token()
	if err != nil {
		return err
	}
	if t != json.Delim('[') {
		count(false)
		return s.Skip(t)
	}
	for s.More() {
		if t, err = s.Token(); err != nil {
			return err
		}
		if t != json.Delim('{') {
			count(false)
			if err = s.Skip(t); err != nil {
				return err
			}
			continue
		}
		if err = appInsightsTable(s, count, row); err != nil {
			return err
		}
	}
	_, err = s.Token() // ']'
	return err
}

// appInsightsTable reads one table after its '{'. Its rows are streamed once its columns are known; rows that come
// before the columns wait for them.
func appInsightsTable(s *jsonStream, count func(bool), row appInsightsRow) error {
	var (
		cols    *tableColumns
		pending []json.RawMessage
	)
	readRow := func(raw json.RawMessage) {
		cells, ok := cols.cells(raw)
		if !ok {
			count(false)
			return
		}
		count(row(func(name string) (string, bool) {
			if i, found := cols.index[strings.ToLower(name)]; found {
				return scalarText(cells[i])
			}
			return "", true
		}))
	}
	for s.More() {
		key, err := s.Key()
		if err != nil {
			return err
		}
		switch {
		case strings.EqualFold(key, "columns"):
			var raw json.RawMessage
			if raw, err = s.Value(); err != nil {
				return err
			}
			cols = newTableColumns(raw)
			for _, r := range pending {
				readRow(r)
			}
			pending = nil
		case strings.EqualFold(key, "rows"):
			t, err := s.Token()
			if err != nil {
				return err
			}
			if t != json.Delim('[') {
				count(false)
				if err = s.Skip(t); err != nil {
					return err
				}
				continue
			}
			for s.More() {
				raw, err := s.Value()
				if err != nil {
					return err
				}
				if cols == nil {
					pending = append(pending, raw)
				} else {
					readRow(raw)
				}
			}
			if _, err = s.Token(); err != nil { // ']'
				return err
			}
		default:
			if err = s.SkipValue(); err != nil {
				return err
			}
		}
	}
	if cols == nil {
		cols = newTableColumns(nil) // no columns: every row is skipped
	}
	for _, r := range pending {
		readRow(r)
	}
	_, err := s.Token() // '}'
	return err
}

// tableColumns maps the lower-case column names of a table to their index; the first of two equal names wins.
type tableColumns struct {
	index map[string]int
	width int // the number of cells a row must have; 0 = the columns could not be read
}

func newTableColumns(raw json.RawMessage) *tableColumns {
	var list []struct {
		Name string `json:"name"`
	}
	c := &tableColumns{index: map[string]int{}}
	if json.Unmarshal(raw, &list) != nil {
		return c
	}
	for i, col := range list {
		if key := strings.ToLower(strings.TrimSpace(col.Name)); key != "" {
			if _, dup := c.index[key]; !dup {
				c.index[key] = i
			}
		}
	}
	c.width = len(list)
	return c
}

// cells splits a row into its cells; ok is false when it is not an array with one cell per column.
func (c *tableColumns) cells(raw json.RawMessage) ([]json.RawMessage, bool) {
	var cells []json.RawMessage
	if json.Unmarshal(raw, &cells) != nil || c.width == 0 || len(cells) != c.width {
		return nil, false
	}
	return cells, true
}

// appInsightsRecord reads one row through get. "None" in any column is null; an empty or null caller is anonymous.
func appInsightsRecord(get func(name string) (string, bool), callerField string) (Record, bool) {
	f := rowFields{get: func(name string) (string, bool) {
		v, ok := get(name)
		if strings.TrimSpace(v) == "None" {
			v = ""
		}
		return v, ok
	}}
	var caller string
	if callerField != "" {
		caller = f.field(callerField)
	} else if caller = f.field(appInsightsCaller); caller == "" {
		caller = f.field(appInsightsClassicCaller)
	}
	r := Record{Caller: caller, Kind: "service", Method: f.field("Method"), Host: f.field("Host"), Path: f.field("Path")}
	calls, lastSeen := f.field("Calls"), f.field("LastSeen")
	if f.bad {
		return Record{}, false
	}
	return tableRecord(r, calls, lastSeen)
}
