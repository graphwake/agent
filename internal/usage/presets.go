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
	Name        string // "apim" | "appinsights" | "datadog" | "generic" | "kong" | "nginx" | "otel"
	DefaultKind string // "client" | "service"; "" for generic, where every row gives its own kind
	// Parse streams r and calls emit for every record. A line or element it cannot read is skipped and counted; its
	// content never appears in the result or the error. err is a read error, or says that the input held something
	// but not one readable line or element (for generic CSV also: no header line).
	Parse func(r io.Reader, opts ParseOptions, emit func(Record)) (ParseResult, error)
	// Query is the text `usage query` prints, ending in a newline: the tool's query or configuration; for generic
	// the CSV header and one example line.
	Query string
}

// ParseResult is what a preset reports besides its records.
type ParseResult struct {
	Skipped int64 // lines or elements it could not read
	Partial bool  // the tool said its export is incomplete (a timeout or a warning); only datadog sets it
}

// ParseOptions changes how a preset reads its input.
type ParseOptions struct {
	CallerField string           // the field that names the caller; "" = the preset's default
	Now         func() time.Time // the clock, for an input that carries no day (datadog); nil = time.Now
}

var presets = map[string]Preset{
	"apim":        apimPreset,
	"appinsights": appInsightsPreset,
	"datadog":     datadogPreset,
	"generic":     genericPreset,
	"kong":        kongPreset,
	"nginx":       nginxPreset,
	"otel":        otelPreset,
}

// presetAliases are the other names a preset is known by. PresetByName accepts them; PresetNames and every message
// use the canonical name.
var presetAliases = map[string]string{
	"app-insights":         "appinsights",
	"applicationinsights":  "appinsights",
	"application-insights": "appinsights",
	"azure-apim":           "apim",
	"api-management":       "apim",
	"opentelemetry":        "otel",
	"otlp":                 "otel",
	"dd":                   "datadog",
	"csv":                  "generic",
	"json":                 "generic",
}

// PresetByName returns the preset with that name or alias, ignoring case and surrounding space.
func PresetByName(name string) (Preset, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if canonical, ok := presetAliases[name]; ok {
		name = canonical
	}
	p, ok := presets[name]
	return p, ok
}

// PresetNames lists the canonical preset names, sorted.
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
	first, ok, err = skipSpace(br)
	return br, first, ok, err
}

// skipSpace reads past white space. first is the next byte, left unread; ok is false at the end of the input.
func skipSpace(br *bufio.Reader) (first byte, ok bool, err error) {
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		}
		_ = br.UnreadByte()
		return b, true, nil
	}
}

// eachJSONValue calls fn for every JSON value of the input, one top-level value after another until the input
// ends: a top-level value that starts with '[' gives its elements, anything else gives the non-blank line it starts
// on. Arrays and lines may follow one another in any number and order (a receiver that appends every http-log batch
// to one file writes arrays one after another). fn reports whether it could read the value; read and skipped count
// the answers. A broken array never ends the input: see eachArrayElement. err is a read error only.
func eachJSONValue(br *bufio.Reader, first byte, fn func(raw []byte) bool) (read, skipped int64, err error) {
	count := func(ok bool) {
		if ok {
			read++
		} else {
			skipped++
		}
	}
	src := &pushbackReader{r: br}
	in := bufio.NewReader(src)
	for {
		if first == '[' {
			more, err := eachArrayElement(in, src, count, fn)
			if err != nil || !more {
				return read, skipped, err
			}
		} else {
			line, err := in.ReadBytes('\n')
			if line = bytes.TrimSpace(line); len(line) > 0 {
				count(fn(line))
			}
			if err == io.EOF {
				return read, skipped, nil
			}
			if err != nil {
				return read, skipped, err
			}
		}
		var ok bool
		if first, ok, err = skipSpace(in); err != nil || !ok {
			return read, skipped, err
		}
	}
}

// eachArrayElement calls fn for every element of the array that starts at in's next byte and counts the answers. When
// the array closes, in goes on right after its ']'. A syntax error inside the array (a stray "[error] ..." line, a
// line cut short) costs one skipped element and the rest of the line the error is on, and in goes on at the next
// line; when the error is the first thing on its line, nothing is dropped or counted and in goes on at that line,
// whose values are then counted on their own. more is false only when the input ended inside the array (one skipped
// element). err is a read error only.
func eachArrayElement(in *bufio.Reader, src *pushbackReader, count func(bool), fn func(raw []byte) bool) (more bool, err error) {
	lines := &newlineTracker{r: in}
	dec := json.NewDecoder(lines)
	if _, err := dec.Token(); err != nil { // the '[' the caller saw
		return false, err
	}
	goodEnd := dec.InputOffset() // the end of the last token read without error
	for dec.More() {
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			break
		}
		count(fn(raw))
		goodEnd = dec.InputOffset()
		lines.forget(goodEnd)
	}
	if err == nil {
		_, err = dec.Token() // the closing ']'
	}
	switch {
	case err == nil:
	case isReadError(err):
		return false, err
	case err == io.EOF || err == io.ErrUnexpectedEOF:
		count(false) // the array is cut short by the end of the input
		return false, nil
	}

	// The decoder reads ahead: put what it has not used (after a syntax error: from the bad value on), then what in
	// still holds, back in front of the input, so that nothing is lost or read twice.
	rest, _ := io.ReadAll(dec.Buffered())
	lineStart := false
	if err != nil {
		// Depending on the Go version, the decoder stops on the bad value itself or before the comma and white space
		// ahead of it: look through both for the line break that makes the bad value the first thing on its line.
		gap := len(rest) - len(bytes.TrimLeft(rest, " \t\r\n,"))
		lineStart = lines.seen(goodEnd, dec.InputOffset()) || bytes.IndexByte(rest[:gap], '\n') >= 0
		rest = rest[gap:]
	}
	held, _ := in.Peek(in.Buffered())
	src.unread(rest, held)
	in.Reset(src)
	if err == nil || lineStart {
		return true, nil
	}
	count(false)
	return true, discardLine(in)
}

// discardLine reads past the next newline, or to the end of the input, without keeping the line. err is a read
// error only.
func discardLine(br *bufio.Reader) error {
	for {
		_, err := br.ReadSlice('\n')
		switch err {
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			return nil
		}
		return err
	}
}

// newlineTracker passes reads through and remembers the offsets (from the first byte it read) of the newlines it
// passed that forget has not dropped yet. Of a run of newlines with only white space between them it keeps the first:
// that is all seen needs, and blank lines cannot make the list grow.
type newlineTracker struct {
	r        io.Reader
	off      int64
	newlines []int64
	blank    bool // nothing but white space since the last kept newline
}

func (t *newlineTracker) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	for i, c := range b[:n] {
		switch c {
		case '\n':
			if !t.blank {
				t.newlines = append(t.newlines, t.off+int64(i))
				t.blank = true
			}
		case ' ', '\t', '\r':
		default:
			t.blank = false
		}
	}
	t.off += int64(n)
	return n, err
}

// forget drops the newlines before off.
func (t *newlineTracker) forget(off int64) {
	i := 0
	for i < len(t.newlines) && t.newlines[i] < off {
		i++
	}
	t.newlines = t.newlines[i:]
}

// seen reports whether a newline it passed lies in [from, to).
func (t *newlineTracker) seen(from, to int64) bool {
	for _, n := range t.newlines {
		if n >= from && n < to {
			return true
		}
	}
	return false
}

// pushbackReader reads the bytes put back with unread before those of r.
type pushbackReader struct {
	pending []byte
	r       io.Reader
}

func (p *pushbackReader) Read(b []byte) (int, error) {
	if len(p.pending) > 0 {
		n := copy(b, p.pending)
		p.pending = p.pending[n:]
		return n, nil
	}
	return p.r.Read(b)
}

// unread puts the parts back, in order, in front of what is still pending.
func (p *pushbackReader) unread(parts ...[]byte) {
	n := len(p.pending)
	for _, part := range parts {
		n += len(part)
	}
	pending := make([]byte, 0, n)
	for _, part := range parts {
		pending = append(pending, part...)
	}
	p.pending = append(pending, p.pending...)
}

// eachJSONObject reads JSON objects one after another until the input ends, with white space, line breaks or nothing
// between them (an answer that a tool appends to a file without a newline is followed directly by the next). For
// every object it calls fn, which walks the object with s, from its '{' up to and including its '}', and counts what
// it reads with count. Each object is streamed: fn reads it one part (a row, a bucket) at a time, so the object may be
// far larger than any part and span any number of lines. A value that does not start with '{' (such as an
// "[error] ..." line) costs one skipped value and the rest of its line. A syntax error inside an object costs one
// skipped value and the rest of the line the error is on; when the error is the first thing on its line, nothing of
// that line is dropped. An object cut short by the end of the input costs one skipped value. err is a read error only.
func eachJSONObject(br *bufio.Reader, first byte, fn func(s *jsonStream, count func(ok bool)) error) (read, skipped int64, err error) {
	count := func(ok bool) {
		if ok {
			read++
		} else {
			skipped++
		}
	}
	src := &pushbackReader{r: br}
	in := bufio.NewReader(src)
	for {
		if first != '{' {
			count(false)
			if err := discardLine(in); err != nil {
				return read, skipped, err
			}
		} else if more, err := streamObject(in, src, count, fn); err != nil || !more {
			return read, skipped, err
		}
		var ok bool
		if first, ok, err = skipSpace(in); err != nil || !ok {
			return read, skipped, err
		}
	}
}

// streamObject runs fn over the object that starts at in's next byte; afterwards in goes on right after the object,
// or after a syntax error where eachJSONObject says. more is false only when the input ended inside the object.
func streamObject(in *bufio.Reader, src *pushbackReader, count func(bool), fn func(*jsonStream, func(bool)) error) (more bool, err error) {
	s := &jsonStream{lines: &newlineTracker{r: in}}
	s.dec = json.NewDecoder(s.lines)
	err = fn(s, count)
	switch {
	case err == nil:
	case isReadError(err):
		return false, err
	case err == io.EOF || err == io.ErrUnexpectedEOF:
		count(false) // the object is cut short by the end of the input
		return false, nil
	default:
		count(false)
	}

	// The decoder reads ahead: put what it has not used (after a syntax error: from the bad token on), then what in
	// still holds, back in front of the input. As in eachArrayElement, the line break that makes the bad token the
	// first thing on its line may lie in what the decoder passed or in what it left.
	rest, _ := io.ReadAll(s.dec.Buffered())
	lineStart := false
	if err != nil {
		gap := len(rest) - len(bytes.TrimLeft(rest, " \t\r\n,"))
		lineStart = s.lines.seen(s.goodEnd, s.dec.InputOffset()) || bytes.IndexByte(rest[:gap], '\n') >= 0
		rest = rest[gap:]
	}
	held, _ := in.Peek(in.Buffered())
	src.unread(rest, held)
	in.Reset(src)
	if err == nil || lineStart {
		return true, nil
	}
	return true, discardLine(in)
}

// jsonStream walks one JSON object token by token. It remembers where the last good token ended, so that after a
// syntax error eachJSONObject can tell whether the bad token starts its line.
type jsonStream struct {
	dec     *json.Decoder
	lines   *newlineTracker
	goodEnd int64
}

// Token reads the next token.
func (s *jsonStream) Token() (json.Token, error) {
	t, err := s.dec.Token()
	if err == nil {
		s.good()
	}
	return t, err
}

// Key reads the name of the next member of an object.
func (s *jsonStream) Key() (string, error) {
	t, err := s.Token()
	key, _ := t.(string)
	return key, err
}

// Value reads the next value whole: an element or member the preset reads at once.
func (s *jsonStream) Value() (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.dec.Decode(&raw)
	if err == nil {
		s.good()
	}
	return raw, err
}

// More reports whether the current array or object has another element or member.
func (s *jsonStream) More() bool { return s.dec.More() }

// Skip reads past the rest of a value whose first token was t, without keeping it.
func (s *jsonStream) Skip(t json.Token) error {
	for depth := 0; ; {
		switch t {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
		var err error
		if t, err = s.Token(); err != nil {
			return err
		}
	}
}

// SkipValue reads past the next value without keeping it.
func (s *jsonStream) SkipValue() error {
	t, err := s.Token()
	if err != nil {
		return err
	}
	return s.Skip(t)
}

func (s *jsonStream) good() {
	s.goodEnd = s.dec.InputOffset()
	s.lines.forget(s.goodEnd)
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
func finish(preset string, read, skipped int64, err error) (ParseResult, error) {
	res := ParseResult{Skipped: skipped}
	if err != nil {
		return res, fmt.Errorf("reading the input: %w", err)
	}
	if read == 0 && skipped > 0 {
		return res, fmt.Errorf("the input has nothing the %s preset can read (%d lines or elements skipped)", preset, skipped)
	}
	return res, nil
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
