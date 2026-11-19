package usage

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxCallerRunes is the server's limit on a caller name; longer names are cut.
const maxCallerRunes = 200

// Aggregator sums records into snapshot rows. Anonymous, withheld and unmatched requests are only counted; their
// names and paths never reach a row.
type Aggregator struct {
	m         *Matcher
	noCounts  bool
	rows      map[rowKey]*rowSum
	unmatched map[PrefixCount]int64 // keyed by Host and Prefix; Count is 0 in the key

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
	return &Aggregator{m: m, noCounts: noCounts, rows: map[rowKey]*rowSum{}, unmatched: map[PrefixCount]int64{}}
}

// Add counts one record. The first rule that applies wins: no caller name (anonymous), a personal-looking name
// (withheld), no endpoint template (unmatched), else the record's row.
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
	}

	e, s, ok := a.m.Match(r)
	if !ok {
		a.unmatchedRequests = AddCount(a.unmatchedRequests, count)
		key := PrefixCount{Host: r.Host, Prefix: pathPrefix(r.Path)}
		a.unmatched[key] = AddCount(a.unmatched[key], count)
		return
	}

	kind := r.Kind
	if kind == "" {
		kind = "client"
	}
	key := rowKey{caller, kind, e.ID}
	sum := a.rows[key]
	if sum == nil {
		sum = &rowSum{row: Row{Caller: caller, CallerKind: kind, EndpointID: e.ID, Service: s.Name, Method: e.Method, Path: e.Path}}
		a.rows[key] = sum
	}
	sum.count = AddCount(sum.count, count)
	if r.Day > sum.row.LastSeen {
		sum.row.LastSeen = r.Day
	}
}

// Snapshot builds the summary to send, rows sorted by caller, service, method, path (then kind and endpoint id).
// It returns a *TooManyCallersError (ErrTooManyCallers) when the distinct callers are over Limits.MaxCallers.
func (a *Aggregator) Snapshot(preset, agentVersion string) (Snapshot, error) {
	rows := make([]Row, 0, len(a.rows))
	callers := map[[2]string]bool{}
	for _, sum := range a.rows {
		row := sum.row
		if !a.noCounts {
			n := sum.count
			row.Count = &n
		}
		rows = append(rows, row)
		callers[[2]string{row.Caller, row.CallerKind}] = true
	}
	if limit := a.m.limits.MaxCallers; len(callers) > limit {
		return Snapshot{}, &TooManyCallersError{Callers: len(callers), Limit: limit}
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

// UnmatchedByPrefix lists the unmatched requests by host and first two path segments, most requests first. It is for
// the local --dry-run table only and is never sent.
func (a *Aggregator) UnmatchedByPrefix() []PrefixCount {
	list := make([]PrefixCount, 0, len(a.unmatched))
	for key, n := range a.unmatched {
		key.Count = n
		list = append(list, key)
	}
	sort.Slice(list, func(i, j int) bool {
		x, y := list[i], list[j]
		if x.Count != y.Count {
			return x.Count > y.Count
		}
		if x.Host != y.Host {
			return x.Host < y.Host
		}
		return x.Prefix < y.Prefix
	})
	return list
}

// pathPrefix keeps the first two segments of a raw path: "/legacy/v2/orders/1" gives "/legacy/v2".
func pathPrefix(p string) string {
	segs := segments(cleanPath(p))
	if len(segs) > 2 {
		segs = segs[:2]
	}
	return "/" + strings.Join(segs, "/")
}

// AddCount adds two request counts (both at least zero), stopping at the largest int64 instead of overflowing.
func AddCount(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
