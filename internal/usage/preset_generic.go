package usage

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// genericColumns is the header of the generic format. caller, method, path and lastSeen are required; callerKind,
// service and count may be left out or empty.
const genericColumns = "caller,callerKind,service,method,path,count,lastSeen"

// genericPreset reads CSV with a header line, a JSON array of objects, or JSON lines, all with the generic columns.
// service is the Graphwake service name; path is a concrete path or a normalized template.
var genericPreset = Preset{
	Name:  "generic",
	Parse: parseGeneric,
	Query: genericColumns + "\ncheckout-web,client,orders,GET,/v1/orders/8812,42,2026-11-18\n",
}

func parseGeneric(r io.Reader, opts ParseOptions, emit func(Record)) (int64, error) {
	callerField := opts.CallerField
	if callerField == "" {
		callerField = "caller"
	}
	br, first, ok, err := openInput(r)
	if err != nil || !ok {
		return finish("generic", 0, 0, err)
	}
	if first != '[' && first != '{' {
		return parseGenericCSV(br, callerField, emit)
	}
	read, skipped, err := eachJSONValue(br, first, func(raw []byte) bool {
		o, ok := decodeObject(raw)
		if !ok {
			return false
		}
		rec, ok := genericRecord(o.get, callerField)
		if ok {
			emit(rec)
		}
		return ok
	})
	return finish("generic", read, skipped, err)
}

// parseGenericCSV reads the CSV form one physical line at a time: a broken line (such as an unclosed quote) costs
// that line alone, and memory stays bounded by the longest line. A quoted field cannot span lines.
func parseGenericCSV(br *bufio.Reader, callerField string, emit func(Record)) (int64, error) {
	var (
		cols          map[string]int // lower-case column name → index; nil until the header is read
		width         int
		read, skipped int64
		headerErr     error
	)
	err := eachLine(br, func(line []byte) bool {
		fields, ok := splitCSVLine(string(line))
		if cols == nil {
			cols, width = map[string]int{}, len(fields)
			for i, name := range fields {
				if key := strings.ToLower(strings.TrimSpace(name)); key != "" {
					if _, dup := cols[key]; !dup {
						cols[key] = i
					}
				}
			}
			for _, name := range []string{callerField, "method", "path", "lastSeen"} {
				if _, found := cols[strings.ToLower(name)]; !found || !ok {
					headerErr = fmt.Errorf("generic CSV input needs a header line with the columns %s (%s, method, path and lastSeen are required)", genericColumns, callerField)
					return false
				}
			}
			return true
		}
		if !ok || len(fields) != width {
			skipped++
			return true
		}
		rec, ok := genericRecord(func(name string) (string, bool) {
			if i, found := cols[strings.ToLower(name)]; found {
				return fields[i], true
			}
			return "", true
		}, callerField)
		if !ok {
			skipped++
			return true
		}
		read++
		emit(rec)
		return true
	})
	if headerErr != nil {
		return 0, headerErr
	}
	return finish("generic", read, skipped, err)
}

// splitCSVLine splits one CSV line with RFC 4180 quoting: a field may be quoted, holding commas and doubled quotes.
// Space before a field is dropped. ok is false for a quote that is not closed, a quote inside an unquoted field, or
// text after a closing quote.
func splitCSVLine(line string) (fields []string, ok bool) {
	for {
		line = strings.TrimLeft(line, " \t")
		var field string
		if strings.HasPrefix(line, `"`) {
			var b strings.Builder
			rest := line[1:]
			for {
				i := strings.IndexByte(rest, '"')
				if i < 0 {
					return nil, false
				}
				b.WriteString(rest[:i])
				rest = rest[i+1:]
				if !strings.HasPrefix(rest, `"`) {
					break
				}
				b.WriteByte('"')
				rest = rest[1:]
			}
			rest = strings.TrimLeft(rest, " \t")
			if rest != "" && rest[0] != ',' {
				return nil, false
			}
			field, line = b.String(), rest
		} else {
			i := strings.IndexByte(line, ',')
			if i < 0 {
				i = len(line)
			}
			field, line = line[:i], line[i:]
			if strings.Contains(field, `"`) {
				return nil, false
			}
		}
		fields = append(fields, field)
		if line == "" {
			return fields, true
		}
		line = line[1:] // the comma
	}
}

// genericRecord reads one row of the generic format through get.
func genericRecord(get func(name string) (string, bool), callerField string) (Record, bool) {
	f := rowFields{get: get}
	r := Record{
		Caller:  f.field(callerField),
		Kind:    strings.ToLower(f.field("callerKind")),
		Service: f.field("service"),
		Method:  f.field("method"),
		Path:    f.field("path"),
	}
	count, lastSeen := f.field("count"), f.field("lastSeen")
	if f.bad || r.Kind != "" && r.Kind != "client" && r.Kind != "service" {
		return Record{}, false
	}
	return tableRecord(r, count, lastSeen)
}
