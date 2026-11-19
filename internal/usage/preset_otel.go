package usage

import (
	"encoding/json"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// otelQuery is the OpenTelemetry Collector configuration that writes HTTP client spans to a file for the otel preset.
const otelQuery = `processors:
  filter/http_client:
    error_mode: ignore
    trace_conditions:
      - span.kind != SPAN_KIND_CLIENT
      - span.attributes["http.request.method"] == nil and span.attributes["http.method"] == nil
exporters:
  file/http_client:
    path: /var/otel/http-client.jsonl
    rotation: {max_megabytes: 100, max_backups: 10}
service:
  pipelines:
    traces/http_client: {receivers: [otlp], processors: [filter/http_client], exporters: [file/http_client]}
`

// otelPreset reads the Collector file exporter's OTLP JSON, one object per line, and turns every HTTP client span
// into one request of the service named by the resource's service.name.
var otelPreset = Preset{Name: "otel", DefaultKind: "service", Parse: parseOtel, Query: otelQuery}

// otlpTraces holds the parts of an OTLP JSON trace export the preset reads; every other member is ignored.
type otlpTraces struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpAttribute `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []otlpSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type otlpSpan struct {
	Kind              json.RawMessage `json:"kind"`              // 3 or "SPAN_KIND_CLIENT"
	StartTimeUnixNano json.RawMessage `json:"startTimeUnixNano"` // a decimal string or a number
	Attributes        []otlpAttribute `json:"attributes"`
}

type otlpAttribute struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string         `json:"stringValue"`
		IntValue    json.RawMessage `json:"intValue"` // a decimal string or a number
	} `json:"value"`
}

// otelCaller is the resource attribute that names the caller by default.
const otelCaller = "service.name"

func parseOtel(r io.Reader, opts ParseOptions, emit func(Record)) (ParseResult, error) {
	callerField := opts.CallerField
	if callerField == "" {
		callerField = otelCaller
	}
	br, first, ok, err := openInput(r)
	if err != nil || !ok {
		return finish("otel", 0, 0, err)
	}
	read, skipped, err := eachJSONValue(br, first, func(raw []byte) bool {
		var t otlpTraces
		if json.Unmarshal(raw, &t) != nil {
			return false
		}
		for _, rs := range t.ResourceSpans {
			caller := attribute(rs.Resource.Attributes, callerField)
			for _, ss := range rs.ScopeSpans {
				for _, span := range ss.Spans {
					if rec, ok := otelRecord(span); ok {
						rec.Caller = caller
						emit(rec)
					}
				}
			}
		}
		return true
	})
	return finish("otel", read, skipped, err)
}

// otelRecord turns an HTTP client span into a record. Other spans, and client spans without a method, a path or a
// start time, give none: they are not HTTP calls the summary can use, and are not counted as skipped.
func otelRecord(s otlpSpan) (Record, bool) {
	if kind, _ := scalarText(s.Kind); kind != "3" && kind != "SPAN_KIND_CLIENT" {
		return Record{}, false
	}
	attr := func(keys ...string) string {
		for _, key := range keys {
			if v := attribute(s.Attributes, key); v != "" {
				return v
			}
		}
		return ""
	}
	r := Record{Kind: "service", Count: 1, Method: strings.ToUpper(attr("http.request.method", "http.method"))}
	if r.Method == "" {
		return Record{}, false
	}

	path := ""
	if full := attr("url.full", "http.url"); full != "" {
		u, err := url.Parse(full)
		if err != nil {
			return Record{}, false
		}
		r.Host, path = hostName(u.Host), u.EscapedPath()
		if path == "" && u.Host != "" {
			path = "/"
		}
	}
	if r.Host == "" {
		r.Host = hostName(attr("server.address", "net.peer.name"))
	}
	if path == "" {
		path = attr("url.path", "http.target")
	}
	var ok bool
	if r.Path, ok = requestPath(path); !ok {
		return Record{}, false
	}

	nanos, _ := scalarText(s.StartTimeUnixNano)
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil || n <= 0 {
		return Record{}, false
	}
	r.Day = time.Unix(0, n).UTC().Format(dayLayout)
	return r, true
}

// attribute returns the text of the first attribute with that key: its stringValue, else its intValue.
func attribute(attrs []otlpAttribute, key string) string {
	for _, a := range attrs {
		if a.Key != key {
			continue
		}
		if a.Value.StringValue != nil {
			return strings.TrimSpace(*a.Value.StringValue)
		}
		v, _ := scalarText(a.Value.IntValue)
		return v
	}
	return ""
}
