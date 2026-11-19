package usage

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Matcher finds the endpoint template a raw request path belongs to. It never guesses across services: once a
// mapping, a server URL host or a pinned service has chosen the service, only that service's templates are tried.
type Matcher struct {
	limits   Limits
	mappings []Mapping
	services []*service
	byID     map[string]*service
	byName   map[string]*service
	pin      *service
}

type service struct {
	Service
	hosts     []hostBase // absolute server URLs
	bases     []string   // every server URL path, longest first, then ""
	endpoints []template
}

type hostBase struct {
	host string // lower case, no port
	base string // URL path without a trailing slash; "" for the root
}

type template struct {
	Endpoint
	segs     []string // "{}" for a parameter
	literals int
}

// NewMatcher prepares the templates. pinService names the service records without a mapping or a known host belong
// to ("" = none); an unknown name is an error.
func NewMatcher(t Templates, pinService string) (*Matcher, error) {
	m := &Matcher{limits: t.Limits, mappings: t.Mappings, byID: map[string]*service{}, byName: map[string]*service{}}
	if m.limits.MaxCallers <= 0 {
		m.limits.MaxCallers = DefaultMaxCallers
	}
	if m.limits.MaxRows <= 0 {
		m.limits.MaxRows = DefaultMaxRows
	}
	for _, s := range t.Services {
		svc := &service{Service: s}
		for _, raw := range s.ServerURLs {
			if strings.Contains(raw, "{") {
				continue // templated server URL: no fixed host or base path
			}
			u, err := url.Parse(raw)
			if err != nil {
				continue
			}
			base := trimSlash(u.Path)
			if u.IsAbs() && u.Hostname() != "" {
				svc.hosts = append(svc.hosts, hostBase{host: strings.ToLower(u.Hostname()), base: base})
			}
			if base != "" && !contains(svc.bases, base) {
				svc.bases = append(svc.bases, base)
			}
		}
		sort.SliceStable(svc.bases, func(i, j int) bool { return len(svc.bases[i]) > len(svc.bases[j]) })
		svc.bases = append(svc.bases, "")
		for _, e := range s.Endpoints {
			segs := segments(cleanPath(e.Path))
			tpl := template{Endpoint: e, segs: segs}
			for i, seg := range segs {
				if isParam(seg) {
					segs[i] = "{}"
				} else {
					tpl.literals++
				}
			}
			svc.endpoints = append(svc.endpoints, tpl)
		}
		m.services = append(m.services, svc)
		m.byID[s.ID] = svc
		m.byName[s.Name] = svc
	}
	if pinService != "" {
		m.pin = m.byName[pinService]
		if m.pin == nil {
			return nil, fmt.Errorf("unknown service %q", pinService)
		}
	}
	return m, nil
}

// Match returns the endpoint template and service of a record, or false when nothing matches.
func (m *Matcher) Match(r Record) (Endpoint, Service, bool) {
	path := cleanPath(r.Path)

	// 1. The most specific mapping decides the service; only its strip prefix applies.
	if mp, ok := m.bestMapping(r, path); ok {
		svc := m.byID[mp.ServiceID]
		if svc == nil {
			return Endpoint{}, Service{}, false
		}
		return svc.match(r.Method, stripPrefix(path, mp.StripPrefix))
	}

	// 2. A known host (any service's absolute server URL) decides alone; the longest fitting base path picks the service.
	if r.Host != "" {
		if e, s, ok, known := m.matchByHost(r, path); known {
			return e, s, ok
		}
	}

	// 3. The service the input names, else the pinned one: each server URL path as a base path, then none.
	svc := m.pin
	if r.Service != "" {
		svc = m.byName[r.Service]
	}
	if svc == nil {
		return Endpoint{}, Service{}, false
	}
	for _, base := range svc.bases {
		if hasSegmentPrefix(path, base) {
			if e, s, ok := svc.match(r.Method, stripPrefix(path, base)); ok {
				return e, s, true
			}
		}
	}
	return Endpoint{}, Service{}, false
}

// bestMapping picks the matching mapping with the most set fields, then the longest path prefix, then the first.
func (m *Matcher) bestMapping(r Record, path string) (Mapping, bool) {
	best, bestFields, found := Mapping{}, 0, false
	for _, mp := range m.mappings {
		fields := 0
		if mp.Host != "" {
			if !strings.EqualFold(mp.Host, r.Host) {
				continue
			}
			fields++
		}
		if mp.PathPrefix != "" {
			if !hasSegmentPrefix(path, mp.PathPrefix) {
				continue
			}
			fields++
		}
		if mp.API != "" {
			if mp.API != r.API {
				continue
			}
			fields++
		}
		if fields == 0 {
			continue // a mapping without conditions matches nothing
		}
		if !found || fields > bestFields || fields == bestFields && len(trimSlash(mp.PathPrefix)) > len(trimSlash(best.PathPrefix)) {
			best, bestFields, found = mp, fields, true
		}
	}
	return best, found
}

// matchByHost decides records whose host is the host of any service's absolute server URL; known is false otherwise,
// so the pin step may try. Among that host's server URLs whose base path the request path starts with (on a segment
// boundary), the longest base path chooses the service, and only that service is tried. No fitting base path, or two
// services sharing the longest one (the customer needs a mapping), is unmatched.
func (m *Matcher) matchByHost(r Record, path string) (e Endpoint, s Service, ok, known bool) {
	var chosen *service
	base, ambiguous := "", false
	bestLen := -1
	for _, svc := range m.services {
		for _, hb := range svc.hosts {
			if !strings.EqualFold(hb.host, r.Host) {
				continue
			}
			known = true
			if !hasSegmentPrefix(path, hb.base) || len(hb.base) < bestLen {
				continue
			}
			if len(hb.base) == bestLen {
				ambiguous = ambiguous || svc != chosen
				continue
			}
			chosen, base, bestLen, ambiguous = svc, hb.base, len(hb.base), false
		}
	}
	if chosen == nil || ambiguous {
		return Endpoint{}, Service{}, false, known
	}
	e, s, ok = chosen.match(r.Method, stripPrefix(path, base))
	return e, s, ok, known
}

// match finds the endpoint of this service with the method and a path that matches segment by segment. Among several,
// the one with the most literal segments wins, then the one whose first literal comes earlier, then the first listed.
func (svc *service) match(method, path string) (Endpoint, Service, bool) {
	segs := segments(path)
	var best *template
	for i := range svc.endpoints {
		tpl := &svc.endpoints[i]
		if !strings.EqualFold(tpl.Method, method) || !tpl.matches(segs) {
			continue
		}
		if best == nil || tpl.literals > best.literals || tpl.literals == best.literals && literalEarlier(tpl.segs, best.segs) {
			best = tpl
		}
	}
	if best == nil {
		return Endpoint{}, Service{}, false
	}
	return best.Endpoint, svc.Service, true
}

func (t *template) matches(segs []string) bool {
	if len(segs) != len(t.segs) {
		return false
	}
	for i, seg := range t.segs {
		if seg == "{}" {
			if segs[i] == "" {
				return false
			}
		} else if seg != segs[i] {
			return false
		}
	}
	return true
}

// literalEarlier reports whether a has a literal segment at the first position where a and b differ in kind.
func literalEarlier(a, b []string) bool {
	for i := range a {
		if (a[i] == "{}") != (b[i] == "{}") {
			return a[i] != "{}"
		}
	}
	return false
}

func isParam(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

// cleanPath gives the path a leading slash and drops one trailing slash ("/" stays).
func cleanPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// trimSlash drops trailing slashes, so "/" and "" both become "".
func trimSlash(p string) string {
	return strings.TrimRight(p, "/")
}

// segments splits a cleaned path into its segments; "/" has none.
func segments(p string) []string {
	if p == "/" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(p, "/"), "/")
}

// hasSegmentPrefix reports whether path starts with prefix on a segment boundary ("/v1" is not a prefix of "/v10").
func hasSegmentPrefix(path, prefix string) bool {
	prefix = trimSlash(prefix)
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

// stripPrefix removes prefix from path when it is a segment-boundary prefix; otherwise path is returned unchanged.
func stripPrefix(path, prefix string) string {
	prefix = trimSlash(prefix)
	if prefix == "" || !hasSegmentPrefix(path, prefix) {
		return path
	}
	if rest := path[len(prefix):]; rest != "" {
		return rest
	}
	return "/"
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
