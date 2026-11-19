package usage

import (
	"io"
	"strings"
)

// apimQuery is the Log Analytics query for Azure API Management gateway logs; its output, from
// `az monitor log-analytics query -o json`, is what the apim preset reads.
const apimQuery = `ApiManagementGatewayLogs
| where TimeGenerated > ago(7d)
| extend u = parse_url(Url)
| extend Path = strcat("/", trim_start(@"/", tostring(u.Path))), Host = tostring(u.Host)
| summarize Calls = count(), LastSeen = max(TimeGenerated) by ApimSubscriptionId, ApiId, Method, Host, Path
`

// apimPreset reads the JSON rows of apimQuery: an array of objects (or one object per line) whose values are all
// strings, with null written as "None".
var apimPreset = Preset{Name: "apim", DefaultKind: "client", Parse: parseApim, Query: apimQuery}

func parseApim(r io.Reader, opts ParseOptions, emit func(Record)) (int64, error) {
	callerField := opts.CallerField
	if callerField == "" {
		callerField = "ApimSubscriptionId"
	}
	br, first, ok, err := openInput(r)
	if err != nil || !ok {
		return finish("apim", 0, 0, err)
	}
	read, skipped, err := eachJSONValue(br, first, func(raw []byte) bool {
		o, ok := decodeObject(raw)
		if !ok {
			return false
		}
		rec, ok := apimRecord(o, callerField)
		if ok {
			emit(rec)
		}
		return ok
	})
	return finish("apim", read, skipped, err)
}

// apimRecord reads one row; "None" or "" in the caller column is an anonymous caller.
func apimRecord(o jsonObject, callerField string) (Record, bool) {
	f := rowFields{get: func(name string) (string, bool) {
		v, ok := o.get(name)
		if strings.TrimSpace(v) == "None" {
			v = ""
		}
		return v, ok
	}}
	r := Record{
		Caller: f.field(callerField),
		Kind:   "client",
		API:    f.field("ApiId"),
		Method: f.field("Method"),
		Host:   f.field("Host"),
		Path:   f.field("Path"),
	}
	calls, lastSeen := f.field("Calls"), f.field("LastSeen")
	if f.bad {
		return Record{}, false
	}
	return tableRecord(r, calls, lastSeen)
}
