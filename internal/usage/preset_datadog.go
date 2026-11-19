package usage

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// datadogQuery is the Datadog API call whose answers the datadog preset reads.
const datadogQuery = `# Datadog APM: count outgoing HTTP calls of one service, last 7 days.
# Run once per calling service (replace orders); append every answer to one file.
curl -s -w '\n' -X POST "https://api.${DD_SITE:-datadoghq.com}/api/v2/spans/analytics/aggregate" \
  -H "Content-Type: application/json" \
  -H "DD-API-KEY: ${DD_API_KEY}" -H "DD-APPLICATION-KEY: ${DD_APP_KEY}" \
  -d '{"data":{"type":"aggregate_request","attributes":{
    "filter":{"from":"now-7d","to":"now","query":"service:orders @span.kind:client"},
    "compute":[{"aggregation":"count","type":"total"}],
    "group_by":[{"facet":"service","limit":1},
      {"facet":"@http.url_details.host","limit":20},
      {"facet":"@http.url_details.path","limit":100},
      {"facet":"@http.method","limit":5}]}}}' >> datadog-usage.json
`

// datadogPreset reads answers of POST /api/v2/spans/analytics/aggregate, any number of them one after another. Every
// bucket of "data" is a group of calls of the service its "by" facets name. An answer carries no day: every record
// gets the day the preset runs (UTC). An answer whose meta reports a status other than "done" or any warning makes
// the result Partial; its buckets are still read. So does an answer cut short: it ends early (a syntax error or the
// end of the input), or a new answer starts where its next bucket belongs.
var datadogPreset = Preset{Name: "datadog", DefaultKind: "service", Parse: parseDatadog, Query: datadogQuery}

// The facets of a bucket's "by" the preset reads, in the order it tries them.
const (
	datadogCaller   = "service"
	datadogMethod   = "@http.method"
	datadogPath     = "@http.url_details.path"
	datadogResource = "resource_name" // "GET /orders/{id}": the path when there is no path facet, the method when there is no method facet
)

var datadogHosts = []string{"@http.url_details.host", "@out.host", "@peer.hostname"}

// httpMethods are the methods a resource name may start with.
var httpMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true}

func parseDatadog(r io.Reader, opts ParseOptions, emit func(Record)) (ParseResult, error) {
	callerField := opts.CallerField
	if callerField == "" {
		callerField = datadogCaller
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	day := now().UTC().Format(dayLayout)
	br, first, ok, err := openInput(r)
	if err != nil || !ok {
		return finish("datadog", 0, 0, err)
	}
	partial := false
	read, skipped, err := eachJSONObject(br, first, func(s *jsonStream, count func(bool)) error {
		err := datadogAnswer(s, count, &partial, func(raw json.RawMessage) bool {
			rec, ok := datadogRecord(raw, callerField, day)
			if ok {
				emit(rec)
			}
			return ok
		})
		var restart *restartError
		if errors.As(err, &restart) || err != nil && !isReadError(err) {
			partial = true // the answer was cut short
		}
		return err
	})
	res, err := finish("datadog", read, skipped, err)
	res.Partial = partial
	return res, err
}

// datadogAnswer reads one answer: its buckets are streamed to bucket and counted, its meta may set partial. An
// object without a "data" array (such as an error answer, {"errors":[...]}) is one skipped value. An object with a
// "data" or "meta" member where a bucket belongs is the next answer: this one was cut short, and the *restartError
// hands the next answer back to be read on its own.
func datadogAnswer(s *jsonStream, count func(bool), partial *bool, bucket func(json.RawMessage) bool) error {
	if _, err := s.Token(); err != nil { // '{'
		return err
	}
	answer := false
	for s.More() {
		key, err := s.Key()
		if err != nil {
			return err
		}
		switch {
		case strings.EqualFold(key, "data"):
			t, err := s.Token()
			if err != nil {
				return err
			}
			if t != json.Delim('[') {
				if err = s.Skip(t); err != nil {
					return err
				}
				continue
			}
			answer = true
			for s.More() {
				raw, err := s.Value()
				if err != nil {
					return err
				}
				if datadogAnswerStart(raw) {
					return &restartError{raw: raw}
				}
				count(bucket(raw))
			}
			if _, err = s.Token(); err != nil { // ']'
				return err
			}
		case strings.EqualFold(key, "meta"):
			raw, err := s.Value()
			if err != nil {
				return err
			}
			if datadogIncomplete(raw) {
				*partial = true
			}
		default:
			if err = s.SkipValue(); err != nil {
				return err
			}
		}
	}
	if _, err := s.Token(); err != nil { // '}'
		return err
	}
	if !answer {
		count(false)
	}
	return nil
}

// datadogAnswerStart reports whether a value is an object with a top-level "data" or "meta" member: the start of an
// answer, never a bucket.
func datadogAnswerStart(raw json.RawMessage) bool {
	o, ok := decodeObject(raw)
	if !ok {
		return false
	}
	for key := range o {
		if strings.EqualFold(key, "data") || strings.EqualFold(key, "meta") {
			return true
		}
	}
	return false
}

// datadogIncomplete reports whether an answer's meta says the answer is incomplete: a status other than "done", or a
// warning. A meta without a status, or one that cannot be read, reports nothing. Nothing of the meta is kept.
func datadogIncomplete(raw json.RawMessage) bool {
	var meta struct {
		Status   *string           `json:"status"`
		Warnings []json.RawMessage `json:"warnings"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return false
	}
	return meta.Status != nil && !strings.EqualFold(strings.TrimSpace(*meta.Status), "done") || len(meta.Warnings) > 0
}

// datadogRecord reads one bucket. The path may be a concrete path or a template; see datadogPath.
func datadogRecord(raw json.RawMessage, callerField, day string) (Record, bool) {
	var b struct {
		Attributes struct {
			By      jsonObject `json:"by"`
			Compute jsonObject `json:"compute"`
		} `json:"attributes"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return Record{}, false
	}
	by := rowFields{get: b.Attributes.By.get}
	r := Record{Caller: by.field(callerField), Kind: "service", Method: strings.ToUpper(by.field(datadogMethod)), Day: day}
	for _, facet := range datadogHosts {
		if r.Host = hostName(by.field(facet)); r.Host != "" {
			break
		}
	}
	resourceMethod, resourcePath := splitResource(by.field(datadogResource))
	if r.Method == "" {
		r.Method = resourceMethod
	}
	if r.Path = by.field(datadogPath); r.Path == "" {
		r.Path = resourcePath
	}
	r.Path = placeholderPath(r.Path)
	count, ok := b.Attributes.Compute.get("c0")
	if by.bad || !ok || r.Method == "" || !strings.HasPrefix(r.Path, "/") {
		return Record{}, false
	}
	if r.Count, ok = wholeCount(count); !ok {
		return Record{}, false
	}
	return r, true
}

// splitResource reads a resource name such as "GET /orders/{id}". method is the upper-case HTTP method it starts with,
// followed by a space ("" otherwise); path is what follows the method when it starts with "/" ("" otherwise).
func splitResource(resource string) (method, path string) {
	m, rest, found := strings.Cut(strings.TrimSpace(resource), " ")
	if m = strings.ToUpper(m); !found || !httpMethods[m] {
		return "", ""
	}
	if rest = strings.TrimSpace(rest); strings.HasPrefix(rest, "/") {
		path = rest
	}
	return m, path
}

// placeholderPath normalizes a Datadog path. Datadog replaces identifiers with "?" in quantized paths, so a segment
// that is exactly "?" is a placeholder: it becomes "{}", a normalized template segment the matcher takes as matching
// a template parameter ("/orders/?" becomes "/orders/{}"). A "?" or "#" inside a segment starts a query string or a
// fragment, which is cut off as in every other preset ("/orders?page=2" becomes "/orders").
func placeholderPath(path string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg == "?" {
			segs[i] = "{}"
			continue
		}
		if j := strings.IndexAny(seg, "?#"); j >= 0 {
			segs[i] = seg[:j]
			segs = segs[:i+1]
			break
		}
	}
	return strings.Join(segs, "/")
}

// wholeCount reads a count that must be a whole number of at least one, written as an integer or as a float.
func wholeCount(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	if n, ok := parseCount(s); ok {
		return n, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 1 || f != math.Trunc(f) || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(f), true
}
