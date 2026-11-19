package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Preset reads one tool's export and turns it into records.
type Preset struct {
	Name        string // "generic" | "apim" | "otel"
	DefaultKind string // "client" | "service"; "" for generic, where every row gives its own kind
	// Parse streams r and calls emit for every record. A line or element it cannot read is skipped and counted; its
	// content never appears in the result or the error. err is a read error, or says that the input held something
	// but not one readable line or element (for generic CSV also: no header line).
	Parse func(r io.Reader, opts ParseOptions, emit func(Record)) (skipped int64, err error)
	// Query is the text `usage query` prints, ending in a newline: the tool's query or configuration; for generic
	// the CSV header and one example line.
	Query string
}

// ParseOptions changes how a preset reads its input.
type ParseOptions struct {
	CallerField string // the field that names the caller; "" = the preset's default
}

var presets = map[string]Preset{
	"apim":    apimPreset,
	"generic": genericPreset,
	"otel":    otelPreset,
}

// PresetByName returns the preset with that name.
func PresetByName(name string) (Preset, bool) {
	p, ok := presets[name]
	return p, ok
}

// PresetNames lists the preset names, sorted.
func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const dayLayout = "2006-01-02"

// openInput wraps r and drops a UTF-8 byte order mark and leading white space. first is the first byte of the
// content; ok is false when there is no content.
func openInput(r io.Reader) (br *bufio.Reader, first byte, ok bool, err error) {
	br = bufio.NewReader(r)
	if bom, _ := br.Peek(3); bytes.Equal(bom, []byte("\xef\xbb\xbf")) {
		_, _ = br.Discard(3)
	}
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			return br, 0, false, nil
		}
		if err != nil {
			return br, 0, false, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		_ = br.UnreadByte()
		return br, b, true, nil
	}
}

// eachJSONValue calls fn for every JSON value of the input: the elements of a top-level array when the input starts
// with '[', else every non-blank line. fn reports whether it could read the value; read and skipped count the
// answers. A syntax error inside an array ends the array (the decoder cannot find the next element) and counts as
// one skipped element. err is a read error only.
func eachJSONValue(br *bufio.Reader, first byte, fn func(raw []byte) bool) (read, skipped int64, err error) {
	count := func(ok bool) {
		if ok {
			read++
		} else {
			skipped++
		}
	}
	if first != '[' {
		err := eachLine(br, func(line []byte) bool {
			count(fn(line))
			return true
		})
		return read, skipped, err
	}

	dec := json.NewDecoder(br)
	if _, err := dec.Token(); err != nil { // the '[' openInput saw
		return 0, 0, err
	}
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if isReadError(err) {
				return read, skipped, err
			}
			return read, skipped + 1, nil
		}
		count(fn(raw))
	}
	if _, err := dec.Token(); err != nil { // the closing ']'
		if isReadError(err) {
			return read, skipped, err
		}
		skipped++ // the array is cut short or broken
	}
	return read, skipped, nil
}

// eachLine calls fn for every non-blank line, trimmed, with no limit on the line length, until fn returns false.
// err is a read error only.
func eachLine(br *bufio.Reader, fn func(line []byte) bool) error {
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 && !fn(line) {
			return nil
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// isReadError tells a failure of the reader from a problem with the JSON text.
func isReadError(err error) bool {
	var syntax *json.SyntaxError
	return !errors.As(err, &syntax) && err != io.EOF && err != io.ErrUnexpectedEOF
}

// finish turns what a parser counted into its result. Errors never carry input content: a read error comes from
// the reader, and the other message holds only the preset name and a count.
func finish(preset string, read, skipped int64, err error) (int64, error) {
	if err != nil {
		return skipped, fmt.Errorf("reading the input: %w", err)
	}
	if read == 0 && skipped > 0 {
		return skipped, fmt.Errorf("the input has nothing the %s preset can read (%d lines or elements skipped)", preset, skipped)
	}
	return skipped, nil
}

// jsonObject is one JSON object of the input.
type jsonObject map[string]json.RawMessage

func decodeObject(raw []byte) (jsonObject, bool) {
	var o jsonObject
	if err := json.Unmarshal(raw, &o); err != nil || o == nil {
		return nil, false
	}
	return o, true
}

// get returns a member as text, looked up by its exact name, else ignoring case. A missing member or null is "".
// ok is false for an object, an array or a boolean.
func (o jsonObject) get(name string) (string, bool) {
	raw, found := o[name]
	if !found {
		for k, v := range o {
			if strings.EqualFold(k, name) {
				raw, found = v, true
				break
			}
		}
	}
	if !found {
		return "", true
	}
	return scalarText(raw)
}

// scalarText returns a JSON string's text or a number as written; null is "". ok is false for anything else.
func scalarText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return "", true
	case raw[0] == '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	case raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9':
		return string(raw), true
	}
	return "", false
}

// rowFields reads the trimmed text fields of one row; bad remembers a field that was not text.
type rowFields struct {
	get func(name string) (string, bool)
	bad bool
}

func (f *rowFields) field(name string) string {
	v, ok := f.get(name)
	if !ok {
		f.bad = true
	}
	return strings.TrimSpace(v)
}

// tableRecord checks and normalizes one row of a table-shaped input (generic, apim): r holds the raw caller, kind,
// method, host, path, API and service; count may be "" (one request); lastSeen is a date or a timestamp.
func tableRecord(r Record, count, lastSeen string) (Record, bool) {
	var ok bool
	r.Method = strings.ToUpper(r.Method)
	r.Host = hostName(r.Host)
	if r.Path, ok = requestPath(r.Path); !ok || r.Method == "" {
		return Record{}, false
	}
	if r.Count, ok = parseCount(count); !ok {
		return Record{}, false
	}
	if r.Day, ok = parseDay(lastSeen); !ok {
		return Record{}, false
	}
	return r, true
}

// requestPath drops the query string and fragment of a raw path; a path must start with "/".
func requestPath(raw string) (string, bool) {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(raw)
	return raw, strings.HasPrefix(raw, "/")
}

// hostName lower-cases a host and drops its user info and port.
func hostName(raw string) string {
	if i := strings.LastIndexByte(raw, '@'); i >= 0 {
		raw = raw[i+1:] // user info
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return strings.ToLower((&url.URL{Host: raw}).Hostname())
}

// parseCount reads a request count; "" is one request, and a count must be at least one.
func parseCount(s string) (int64, bool) {
	if s == "" {
		return 1, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n >= 1
}

// parseDay returns the UTC day of an RFC 3339 timestamp, else the day the first 10 characters name.
func parseDay(s string) (string, bool) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(dayLayout), true
	}
	if len(s) >= len(dayLayout) {
		if t, err := time.Parse(dayLayout, s[:len(dayLayout)]); err == nil {
			return t.Format(dayLayout), true
		}
	}
	return "", false
}
