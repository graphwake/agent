package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// kongQuery is the Kong plugin configuration that writes the log the kong preset reads.
const kongQuery = `# Kong: log requests with the file-log plugin, then send the file.
# Each line is one JSON object; Graphwake reads consumer, request.method, request.uri,
# request.headers.host and started_at, and nothing else.
plugins:
  - name: file-log
    config:
      path: /var/log/kong/usage.log
`

// kongPreset reads Kong's log entries: the file-log plugin's output (one JSON object per line) or an http-log batch
// (a JSON array of the same objects). Every entry is one request of the consumer that made it.
var kongPreset = Preset{Name: "kong", DefaultKind: "client", Parse: parseKong, Query: kongQuery}

// The members of a Kong log entry the preset reads, as indexes into kongPaths. Nothing else of an entry is kept:
// the other request headers, request.querystring, client_ip and authenticated_entity are skipped unread.
const (
	kongUsername = iota
	kongCustomID
	kongConsumerID
	kongMethod
	kongURI
	kongURL // only its host is used, when there is no host header
	kongHost
	kongStartedAt
	kongCallerField // the --caller-field path, appended when one is given
)

var kongPaths = [][]string{
	kongUsername:   {"consumer", "username"},
	kongCustomID:   {"consumer", "custom_id"},
	kongConsumerID: {"consumer", "id"},
	kongMethod:     {"request", "method"},
	kongURI:        {"request", "uri"},
	kongURL:        {"request", "url"},
	kongHost:       {"request", "headers", "host"},
	kongStartedAt:  {"started_at"},
}

func parseKong(r io.Reader, opts ParseOptions, emit func(Record)) (int64, error) {
	paths := kongPaths
	if opts.CallerField != "" { // a dotted path, such as consumer.custom_id or request.headers.x-client-id
		paths = append(paths[:len(paths):len(paths)], strings.Split(opts.CallerField, "."))
	}
	br, first, ok, err := openInput(r)
	if err != nil || !ok {
		return finish("kong", 0, 0, err)
	}
	read, skipped, err := eachJSONValue(br, first, func(raw []byte) bool {
		rec, ok := kongRecord(raw, paths)
		if ok {
			emit(rec)
		}
		return ok
	})
	return finish("kong", read, skipped, err)
}

// kongRecord reads one log entry. The caller is the --caller-field value when one is given, else consumer.username,
// custom_id or id, the first that is not empty; an entry without a consumer is anonymous.
func kongRecord(raw []byte, paths [][]string) (Record, bool) {
	v, ok := pickScalars(raw, paths)
	if !ok {
		return Record{}, false
	}
	r := Record{Kind: "client", Method: v[kongMethod], Path: v[kongURI], Host: v[kongHost]}
	if len(paths) > kongCallerField {
		r.Caller = strings.TrimSpace(v[kongCallerField])
	} else {
		for _, c := range []string{v[kongUsername], v[kongCustomID], v[kongConsumerID]} {
			if r.Caller = strings.TrimSpace(c); r.Caller != "" {
				break
			}
		}
	}
	if strings.TrimSpace(r.Host) == "" {
		if u, err := url.Parse(v[kongURL]); err == nil {
			r.Host = u.Host
		}
	}
	ms, err := strconv.ParseInt(v[kongStartedAt], 10, 64)
	if err != nil || ms <= 0 {
		return Record{}, false
	}
	return tableRecord(r, "", time.UnixMilli(ms).UTC().Format(dayLayout))
}

// pickScalars reads one JSON object and returns the text of the value at each path of member names (compared
// ignoring case): a string, or a number as written; a missing member or null is "", and an array (a repeated
// header) gives its first value. Every member no path names is skipped without being kept. ok is false when raw is
// not exactly one JSON object, or a path names an object or a boolean.
func pickScalars(raw []byte, paths [][]string) ([]string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	vals := make([]string, len(paths))
	all := make([]int, len(paths))
	for i := range all {
		all[i] = i
	}
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') || !pickMembers(dec, paths, all, 0, vals) {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF { // anything after the object
		return nil, false
	}
	return vals, true
}

// pickMembers reads the members of an object whose '{' was read, up to and including its '}'. live are the paths
// that run through the object; depth is the index of the path element that names one of its members.
func pickMembers(dec *json.Decoder, paths [][]string, live []int, depth int, vals []string) bool {
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return false
		}
		key, _ := t.(string)
		var here, deeper []int
		for _, i := range live {
			if strings.EqualFold(paths[i][depth], key) {
				if depth == len(paths[i])-1 {
					here = append(here, i)
				} else {
					deeper = append(deeper, i)
				}
			}
		}
		if !pickValue(dec, paths, here, deeper, depth+1, vals) {
			return false
		}
	}
	t, err := dec.Token()
	return err == nil && t == json.Delim('}')
}

// pickValue reads one member value: here are the paths that end at it, deeper the paths that continue into it.
func pickValue(dec *json.Decoder, paths [][]string, here, deeper []int, depth int, vals []string) bool {
	t, err := dec.Token()
	if err != nil {
		return false
	}
	switch t {
	case json.Delim('{'):
		if len(here) > 0 {
			return false
		}
		return pickMembers(dec, paths, deeper, depth, vals)
	case json.Delim('['):
		first := true
		for dec.More() {
			if first && len(here) > 0 {
				if !pickValue(dec, paths, here, nil, depth, vals) {
					return false
				}
			} else if !pickValue(dec, paths, nil, nil, depth, vals) { // skipped
				return false
			}
			first = false
		}
		t, err := dec.Token()
		return err == nil && t == json.Delim(']')
	}
	var text string
	switch s := t.(type) {
	case string:
		text = s
	case json.Number:
		text = s.String()
	case nil:
	default: // a boolean
		if len(here) > 0 {
			return false
		}
	}
	for _, i := range here {
		vals[i] = text
	}
	return true
}
