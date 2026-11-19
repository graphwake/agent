// Package usage turns gateway logs and trace exports into the aggregated summary Graphwake's POST /api/ingest/usage
// takes: which caller used which endpoint template, how often and when last. Raw paths, ids and personal-looking
// caller names never leave this package in the summary; they only feed local counters.
package usage

import (
	"errors"
	"fmt"

	"github.com/graphwake/agent/internal/ingest"
)

// The agent's own limits, used when the templates carry none (a missing or zero member). They equal the server's.
const (
	DefaultMaxCallers = 2000
	DefaultMaxRows    = 50000
)

// Record is one request, or one pre-aggregated group of requests, read from the input.
type Record struct {
	Caller  string // raw caller name from the input; "" means anonymous
	Kind    string // "client" | "service"
	Method  string // upper case
	Host    string // lower case, no port; may be ""
	Path    string // raw path, query string already removed by the preset
	API     string // tool's API name (APIM ApiId); may be ""
	Service string // Graphwake service name given by the input itself (generic preset); "" otherwise
	Count   int64  // >= 1
	Day     string // "2006-01-02" (UTC)
}

// Templates is the answer of GET /api/ingest/usage/templates: the source, its limits and mappings, and the endpoint
// templates of every service the records may be matched to. A missing member decodes to the zero value.
type Templates struct {
	Source   SourceInfo `json:"source"`
	Limits   Limits     `json:"limits"`
	Mappings []Mapping  `json:"mappings"`
	Services []Service  `json:"services"`
}

// SourceInfo names the usage source the key belongs to.
type SourceInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Preset string `json:"preset"`
}

// Limits are the server's limits for one snapshot. Zero means the server sent none: DefaultMaxCallers and
// DefaultMaxRows apply then.
type Limits struct {
	MaxCallers int `json:"maxCallers"`
	MaxRows    int `json:"maxRows"`
}

// Mapping routes gateway traffic to a service. Every non-empty field of Host, PathPrefix and API must match; the
// matched path then loses StripPrefix. An empty field is not set.
type Mapping struct {
	Host        string `json:"host"`
	PathPrefix  string `json:"pathPrefix"`
	API         string `json:"api"`
	ServiceID   string `json:"serviceId"`
	StripPrefix string `json:"stripPrefix"`
}

// Service is a Graphwake service with its server URLs and endpoint templates.
type Service struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ServerURLs []string   `json:"serverUrls"`
	Endpoints  []Endpoint `json:"endpoints"`
}

// Endpoint is one endpoint template. Path is normalized: every parameter is "{}", as in "/orders/{}".
type Endpoint struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Snapshot is the body of POST /api/ingest/usage. Its field set is CLOSED: everything in it leaves the machine, so a
// new member is a privacy decision, and TestSnapshot_field_list_is_closed fails until it is made deliberately.
type Snapshot struct {
	Preset            string `json:"preset"`
	AgentVersion      string `json:"agentVersion"`
	WindowStart       string `json:"windowStart"` // day
	WindowEnd         string `json:"windowEnd"`   // day
	CountsIncluded    bool   `json:"countsIncluded"`
	UnmatchedRequests int64  `json:"unmatchedRequests"`
	WithheldRequests  int64  `json:"withheldRequests"`
	AnonymousRequests int64  `json:"anonymousRequests"`
	Rows              []Row  `json:"rows"`

	// maxRows is Limits.MaxRows of the templates the snapshot was built from (0 = DefaultMaxRows). It is not
	// exported, so it is never encoded.
	maxRows int
}

// Row is one (caller, endpoint template) pair of the snapshot.
type Row struct {
	Caller     string `json:"caller"`
	CallerKind string `json:"callerKind"`
	EndpointID string `json:"endpointId"`
	Service    string `json:"service"`
	Method     string `json:"method"`
	Path       string `json:"path"`            // normalized template
	Count      *int64 `json:"count,omitempty"` // nil with --no-counts
	LastSeen   string `json:"lastSeen"`        // day
}

// PrefixCount is one line of the local, never-sent table of unmatched requests: the host and the first two path
// segments of the raw path, with the number of requests.
type PrefixCount struct {
	Host   string
	Prefix string
	Count  int64
	// Other marks the one entry that counts the requests of every host and prefix past the table's cap
	// (maxUnmatchedKeys); its Host and Prefix are empty.
	Other bool
}

var (
	// ErrTooManyCallers: the input has more distinct callers than Limits.MaxCallers. The returned error is a
	// *TooManyCallersError that wraps it.
	ErrTooManyCallers = errors.New("too many distinct callers")
	// ErrTooLarge: the encoded snapshot is over 10 MB or has more rows than Limits.MaxRows.
	ErrTooLarge = errors.New("usage summary too large, not sent")
)

// TooManyCallersError carries the caller limit the input went over. The agent stops counting callers there, so
// the number is only ever "more than Limit".
type TooManyCallersError struct {
	Limit int
}

func (e *TooManyCallersError) Error() string {
	return fmt.Sprintf("%v (more than %d, limit %d)", ErrTooManyCallers, e.Limit, e.Limit)
}

func (e *TooManyCallersError) Unwrap() error { return ErrTooManyCallers }

// Encode marshals the snapshot exactly as it is sent and enforces the row limit and the 10 MB limit on the result.
func Encode(s Snapshot) ([]byte, error) {
	maxRows := s.maxRows
	if maxRows <= 0 {
		maxRows = DefaultMaxRows
	}
	if len(s.Rows) > maxRows {
		return nil, ErrTooLarge
	}
	if s.Rows == nil {
		s.Rows = []Row{}
	}
	data, err := ingest.EncodeJSON(s)
	if errors.Is(err, ingest.ErrOverMaxBody) {
		return nil, ErrTooLarge
	}
	return data, err
}
