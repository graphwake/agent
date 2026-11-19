package usage

import (
	"bufio"
	"encoding/csv"
	"errors"
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

func parseGenericCSV(br *bufio.Reader, callerField string, emit func(Record)) (int64, error) {
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		if !isCSVError(err) {
			return finish("generic", 0, 0, err)
		}
		header = nil // a broken first line is no header
	}
	cols := map[string]int{}
	for i, name := range header {
		if key := strings.ToLower(strings.TrimSpace(name)); key != "" {
			if _, dup := cols[key]; !dup {
				cols[key] = i
			}
		}
	}
	for _, name := range []string{callerField, "method", "path", "lastSeen"} {
		if _, ok := cols[strings.ToLower(name)]; !ok {
			return 0, fmt.Errorf("generic CSV input needs a header line with the columns %s (%s, method, path and lastSeen are required)", genericColumns, callerField)
		}
	}
	width := len(header)

	var read, skipped int64
	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if isCSVError(err) {
				skipped++
				continue
			}
			return finish("generic", read, skipped, err)
		}
		if len(row) != width {
			skipped++
			continue
		}
		rec, ok := genericRecord(func(name string) (string, bool) {
			if i, ok := cols[strings.ToLower(name)]; ok {
				return row[i], true
			}
			return "", true
		}, callerField)
		if !ok {
			skipped++
			continue
		}
		read++
		emit(rec)
	}
	return finish("generic", read, skipped, nil)
}

func isCSVError(err error) bool {
	var parse *csv.ParseError
	return errors.As(err, &parse)
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
