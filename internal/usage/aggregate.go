package usage

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxCallerRunes is the server's limit on a caller name; longer names are cut.
const maxCallerRunes = 200

// maxUnmatchedKeys caps the local table of unmatched requests: the requests of any further host and prefix are
// counted in one "(other)" entry.
const maxUnmatchedKeys = 2000

// maxHostBytes is the longest host the local table of unmatched requests keeps as a key (the longest DNS name is 253
// characters); the requests of a longer host are counted in the "(other)" entry, so neither the table's memory nor
// the printed table grows with the size of a host.
const maxHostBytes = 255

// Aggregator sums records into snapshot rows. Anonymous, withheld and unmatched requests add no row and are only
// counted in the summary; nothing of their caller names or paths is sent. The host and the masked path prefix of
// unmatched requests are kept for the --dry-run table alone (see pathPrefix, maxUnmatchedKeys and maxHostBytes).
type Aggregator struct {
	m         *Matcher
	noCounts  bool
	rows      map[rowKey]*rowSum
	callers   map[[2]string]bool    // the distinct caller name and kind pairs of rows
	unmatched map[PrefixCount]int64 // keyed by Host and Prefix; Count is 0 in the key

	unmatchedOther                                         int64 // requests past maxUnmatchedKeys or with a host over maxHostBytes
	tooManyCallers, tooManyRows                            bool  // a limit was passed: no row was kept for it
	unmatchedRequests, withheldRequests, anonymousRequests int64
	windowStart, windowEnd                                 string
}

type rowKey struct{ caller, kind, endpointID string }

type rowSum struct {
	row   Row
	count int64
}

// NewAggregator starts an empty summary. With noCounts the snapshot carries no request counts.
func NewAggregator(m *Matcher, noCounts bool) *Aggregator {
	return &Aggregator{m: m, noCounts: noCounts, rows: map[rowKey]*rowSum{}, callers: map[[2]string]bool{},
		unmatched: map[PrefixCount]int64{}}
}

// Add counts one record. A caller that is a certificate subject is first reduced to its common name (see
// subjectCommonName). Then the first rule that applies wins: no caller name (anonymous), a name the guard withholds
// before or after it is cut to maxCallerRunes (withheld), no endpoint template (unmatched), else the record's row.
func (a *Aggregator) Add(r Record) {
	count := r.Count
	if count < 1 {
		count = 1
	}
	if r.Day != "" {
		if a.windowStart == "" || r.Day < a.windowStart {
			a.windowStart = r.Day
		}
		if r.Day > a.windowEnd {
			a.windowEnd = r.Day
		}
	}

	caller := strings.TrimSpace(r.Caller)
	if cn, ok := subjectCommonName(caller); ok {
		caller = cn
	}
	if caller == "" {
		a.anonymousRequests = AddCount(a.anonymousRequests, count)
		return
	}
	if LooksPersonal(caller) {
		a.withheldRequests = AddCount(a.withheldRequests, count)
		return
	}
	if utf8.RuneCountInString(caller) > maxCallerRunes {
		caller = strings.TrimSpace(string([]rune(caller)[:maxCallerRunes]))
		// The cut can make a name the guard withholds ("...10.0.0.1299" becomes "...10.0.0.1"): check it again.
		if LooksPersonal(caller) {
			a.withheldRequests = AddCount(a.withheldRequests, count)
			return
		}
	}

	e, s, ok := a.m.Match(r)
	if !ok {
		a.unmatchedRequests = AddCount(a.unmatchedRequests, count)
		if len(r.Host) > maxHostBytes {
			a.unmatchedOther = AddCount(a.unmatchedOther, count)
			return
		}
		key := PrefixCount{Host: r.Host, Prefix: pathPrefix(r.Path)}
		if _, known := a.unmatched[key]; known || len(a.unmatched) < maxUnmatchedKeys {
			a.unmatched[key] = AddCount(a.unmatched[key], count)
		} else {
			a.unmatchedOther = AddCount(a.unmatchedOther, count)
		}
		return
	}

	kind := r.Kind
	if kind == "" {
		kind = "client"
	}
	key := rowKey{caller, kind, e.ID}
	sum := a.rows[key]
	if sum == nil {
		// Past a limit nothing more is kept: the snapshot will be refused, and memory stays bounded.
		if c := [2]string{caller, kind}; !a.callers[c] {
			if len(a.callers) >= a.m.limits.MaxCallers {
				a.tooManyCallers = true
				return
			}
			a.callers[c] = true
		}
		if len(a.rows) >= a.m.limits.MaxRows {
			a.tooManyRows = true
			return
		}
		sum = &rowSum{row: Row{Caller: caller, CallerKind: kind, EndpointID: e.ID, Service: s.Name, Method: e.Method, Path: e.Path}}
		a.rows[key] = sum
	}
	sum.count = AddCount(sum.count, count)
	if r.Day > sum.row.LastSeen {
		sum.row.LastSeen = r.Day
	}
}

// Snapshot builds the summary to send, rows sorted by caller, service, method, path (then kind and endpoint id).
// It returns a *TooManyCallersError (ErrTooManyCallers) when the distinct callers went over Limits.MaxCallers, and
// ErrTooLarge when the rows went over Limits.MaxRows.
func (a *Aggregator) Snapshot(preset, agentVersion string) (Snapshot, error) {
	if a.tooManyCallers {
		return Snapshot{}, &TooManyCallersError{Limit: a.m.limits.MaxCallers}
	}
	if a.tooManyRows {
		return Snapshot{}, ErrTooLarge
	}
	rows := make([]Row, 0, len(a.rows))
	for _, sum := range a.rows {
		row := sum.row
		if !a.noCounts {
			n := sum.count
			row.Count = &n
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		x, y := rows[i], rows[j]
		for _, pair := range [][2]string{
			{x.Caller, y.Caller}, {x.Service, y.Service}, {x.Method, y.Method}, {x.Path, y.Path},
			{x.CallerKind, y.CallerKind}, {x.EndpointID, y.EndpointID},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})
	return Snapshot{
		Preset:            preset,
		AgentVersion:      agentVersion,
		WindowStart:       a.windowStart,
		WindowEnd:         a.windowEnd,
		CountsIncluded:    !a.noCounts,
		UnmatchedRequests: a.unmatchedRequests,
		WithheldRequests:  a.withheldRequests,
		AnonymousRequests: a.anonymousRequests,
		Rows:              rows,
		maxRows:           a.m.limits.MaxRows,
	}, nil
}

// UnmatchedByPrefix lists the unmatched requests by host and first two path segments, most requests first, with
// the "(other)" entry (Other) for the hosts and prefixes past maxUnmatchedKeys and the hosts longer than
// maxHostBytes. It is for the local --dry-run table only and is never sent.
func (a *Aggregator) UnmatchedByPrefix() []PrefixCount {
	list := make([]PrefixCount, 0, len(a.unmatched)+1)
	for key, n := range a.unmatched {
		key.Count = n
		list = append(list, key)
	}
	if a.unmatchedOther > 0 {
		list = append(list, PrefixCount{Count: a.unmatchedOther, Other: true})
	}
	sort.Slice(list, func(i, j int) bool {
		x, y := list[i], list[j]
		if x.Count != y.Count {
			return x.Count > y.Count
		}
		if x.Other != y.Other {
			return y.Other // on a tie, "(other)" comes last
		}
		if x.Host != y.Host {
			return x.Host < y.Host
		}
		return x.Prefix < y.Prefix
	})
	return list
}

// pathPrefix keeps the first two segments of a raw path for the unmatched table: "/legacy/orders/1" gives
// "/legacy/orders". A segment that may be an id, an address, an encoded value or a token is written "*" (see
// maskSegment): "/users/jane@contoso.com" gives "/users/*", "/v1/orders/8812" gives "/*/orders".
func pathPrefix(p string) string {
	segs := segments(cleanPath(p))
	if len(segs) > 2 {
		segs = segs[:2]
	}
	for i, seg := range segs {
		segs[i] = maskSegment(seg)
	}
	return "/" + strings.Join(segs, "/")
}

// maxSegmentRunes is the longest path segment the unmatched table shows as it is.
const maxSegmentRunes = 24

// maskSegment returns "*" for a segment with a digit, "@" or "%", or more than maxSegmentRunes characters, and the
// segment itself otherwise.
func maskSegment(seg string) string {
	if strings.ContainsAny(seg, "0123456789@%") || utf8.RuneCountInString(seg) > maxSegmentRunes {
		return "*"
	}
	return seg
}

// AddCount adds two request counts (both at least zero), stopping at the largest int64 instead of overflowing.
func AddCount(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
