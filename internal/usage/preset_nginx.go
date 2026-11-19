package usage

import "io"

// nginxQuery is the NGINX log format that writes the access log the nginx preset reads.
const nginxQuery = `# NGINX: add this log format (http context), then send the file.
# "caller" is the value that identifies the client: a header you set at the edge,
# $remote_user for Basic auth, or $ssl_client_s_dn for mutual TLS.
log_format graphwake_usage escape=json
  '{"time":"$time_iso8601","method":"$request_method",'
  '"host":"$host","path":"$uri","caller":"$http_x_client_id"}';
access_log /var/log/nginx/usage.log graphwake_usage;
`

// nginxPreset reads an access log written in nginxQuery's format, one JSON object per line. A line in another
// format (such as combined) is skipped and counted.
var nginxPreset = Preset{Name: "nginx", DefaultKind: "client", Parse: parseNginx, Query: nginxQuery}

func parseNginx(r io.Reader, opts ParseOptions, emit func(Record)) (ParseResult, error) {
	callerField := opts.CallerField
	if callerField == "" {
		callerField = "caller"
	}
	br, _, ok, err := openInput(r)
	if err != nil || !ok {
		return finish("nginx", 0, 0, err)
	}
	// Line by line, never as one JSON array: the log is lines, and a line that starts with '[' is just a bad line.
	var read, skipped int64
	err = eachLine(br, func(line []byte) bool {
		if line == nil { // longer than maxLineBytes
			skipped++
		} else if rec, ok := nginxRecord(line, callerField); ok {
			emit(rec)
			read++
		} else {
			skipped++
		}
		return true
	})
	return finish("nginx", read, skipped, err)
}

// nginxRecord reads one line; an empty caller or "-" (NGINX's mark for a missing value) is anonymous.
func nginxRecord(line []byte, callerField string) (Record, bool) {
	o, ok := decodeObject(line)
	if !ok {
		return Record{}, false
	}
	f := rowFields{get: o.get}
	r := Record{
		Caller: f.field(callerField),
		Kind:   "client",
		Method: f.field("method"),
		Host:   f.field("host"),
		Path:   f.field("path"),
	}
	day := f.field("time")
	if f.bad {
		return Record{}, false
	}
	if r.Caller == "-" {
		r.Caller = ""
	}
	return tableRecord(r, "", day)
}
