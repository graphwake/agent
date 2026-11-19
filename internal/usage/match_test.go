package usage

import "testing"

// shop returns templates with two services: orders (s1) and billing (s2).
func shop(mappings ...Mapping) Templates {
	return Templates{
		Source:   SourceInfo{ID: "src", Name: "gateway", Preset: "apim"},
		Mappings: mappings,
		Services: []Service{
			{ID: "s1", Name: "orders", ServerURLs: []string{"https://orders.internal/v1"}, Endpoints: []Endpoint{
				{ID: "e1", Method: "GET", Path: "/orders/{}"},
				{ID: "e2", Method: "GET", Path: "/orders/export"},
				{ID: "e3", Method: "POST", Path: "/orders"},
				{ID: "e4", Method: "GET", Path: "/"},
			}},
			{ID: "s2", Name: "billing", ServerURLs: []string{"https://billing.internal", "/api"}, Endpoints: []Endpoint{
				{ID: "e10", Method: "GET", Path: "/invoices/{}"},
				{ID: "e11", Method: "GET", Path: "/invoices/{}/lines/{}"},
			}},
		},
	}
}

func mustMatcher(t *testing.T, tpl Templates, pin string) *Matcher {
	t.Helper()
	m, err := NewMatcher(tpl, pin)
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	return m
}

type matchCase struct {
	name     string
	rec      Record
	endpoint string // "" = unmatched
	service  string
}

func runMatchCases(t *testing.T, m *Matcher, cases []matchCase) {
	t.Helper()
	for _, c := range cases {
		e, s, ok := m.Match(c.rec)
		if c.endpoint == "" {
			if ok {
				t.Errorf("%s: matched %s %s (%s), want unmatched", c.name, e.Method, e.Path, s.Name)
			}
			continue
		}
		if !ok || e.ID != c.endpoint || s.Name != c.service {
			t.Errorf("%s: got %q in %q (ok=%v), want %q in %q", c.name, e.ID, s.Name, ok, c.endpoint, c.service)
		}
	}
}

func TestMatch_mapping_routes_gateway_path(t *testing.T) {
	m := mustMatcher(t, shop(Mapping{Host: "api.contoso.com", PathPrefix: "/shop", ServiceID: "s1", StripPrefix: "/shop/v1"}), "")
	runMatchCases(t, m, []matchCase{
		{"strip then match", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/v1/orders/8812"}, "e1", "orders"},
		{"trailing slash ignored", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/v1/orders/8812/"}, "e1", "orders"},
		{"host ignores case", Record{Method: "GET", Host: "API.Contoso.com", Path: "/shop/v1/orders/8812"}, "e1", "orders"},
		{"prefix on segment boundary only", Record{Method: "GET", Host: "api.contoso.com", Path: "/shopping/v1/orders/8812"}, "", ""},
		{"literal segments are case-sensitive", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/v1/Orders/8812"}, "", ""},
		{"method must match", Record{Method: "DELETE", Host: "api.contoso.com", Path: "/shop/v1/orders/8812"}, "", ""},
		{"placeholder needs a segment", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/v1/orders//"}, "", ""},
	})

	// Only the mapping's strip applies: the server URL base path /v1 is not tried as well.
	m = mustMatcher(t, shop(Mapping{Host: "api.contoso.com", PathPrefix: "/shop", ServiceID: "s1", StripPrefix: "/shop"}), "")
	runMatchCases(t, m, []matchCase{
		{"mapping strip only", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/v1/orders/8812"}, "", ""},
	})

	// The most specific mapping wins: more fields first, then the longer path prefix.
	m = mustMatcher(t, shop(
		Mapping{Host: "api.contoso.com", ServiceID: "s2"},
		Mapping{Host: "api.contoso.com", PathPrefix: "/shop", ServiceID: "s2", StripPrefix: "/shop"},
		Mapping{Host: "api.contoso.com", PathPrefix: "/shop/v1", ServiceID: "s1", StripPrefix: "/shop/v1"},
	), "")
	runMatchCases(t, m, []matchCase{
		{"longer prefix wins", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/v1/orders/1"}, "e1", "orders"},
		{"shorter prefix", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/invoices/1"}, "e10", "billing"},
		{"host only", Record{Method: "GET", Host: "api.contoso.com", Path: "/invoices/1"}, "e10", "billing"},
	})
}

func TestMatch_server_url_base_path(t *testing.T) {
	m := mustMatcher(t, shop(), "")
	runMatchCases(t, m, []matchCase{
		{"base path removed", Record{Method: "GET", Host: "orders.internal", Path: "/v1/orders/42"}, "e1", "orders"},
		{"base path itself is the root", Record{Method: "GET", Host: "orders.internal", Path: "/v1"}, "e4", "orders"},
		{"empty base path", Record{Method: "GET", Host: "billing.internal", Path: "/invoices/7"}, "e10", "billing"},
		{"base path on segment boundary", Record{Method: "GET", Host: "orders.internal", Path: "/v10/orders/42"}, "", ""},
		{"missing base path", Record{Method: "GET", Host: "orders.internal", Path: "/orders/42"}, "", ""},
		{"unknown host", Record{Method: "GET", Host: "elsewhere.internal", Path: "/v1/orders/42"}, "", ""},
		{"no host", Record{Method: "GET", Path: "/v1/orders/42"}, "", ""},
	})
}

func TestMatch_shared_host_longest_base_path(t *testing.T) {
	// The longer base path chooses the service, even where the shorter one has a matching template.
	tpl := Templates{Services: []Service{
		{ID: "root", Name: "gateway-root", ServerURLs: []string{"https://api.contoso.com"}, Endpoints: []Endpoint{
			{ID: "r1", Method: "GET", Path: "/shop/orders/{}"},
			{ID: "r2", Method: "GET", Path: "/health"},
		}},
		{ID: "shop", Name: "shop", ServerURLs: []string{"https://api.contoso.com/shop"}, Endpoints: []Endpoint{
			{ID: "s1", Method: "GET", Path: "/carts/{}"},
		}},
	}}
	runMatchCases(t, mustMatcher(t, tpl, ""), []matchCase{
		{"longer base path wins, its template miss is unmatched", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/orders/1"}, "", ""},
		{"longer base path wins", Record{Method: "GET", Host: "api.contoso.com", Path: "/shop/carts/1"}, "s1", "shop"},
		{"only the shorter base path fits", Record{Method: "GET", Host: "api.contoso.com", Path: "/health"}, "r2", "gateway-root"},
	})

	// Two services with the same base path on one host: the record needs a mapping.
	tpl = Templates{Services: []Service{
		{ID: "a", Name: "a", ServerURLs: []string{"https://api.contoso.com/v1"}, Endpoints: []Endpoint{{ID: "a1", Method: "GET", Path: "/orders/{}"}}},
		{ID: "b", Name: "b", ServerURLs: []string{"https://api.contoso.com/v1/"}, Endpoints: []Endpoint{{ID: "b1", Method: "GET", Path: "/carts/{}"}}},
	}}
	runMatchCases(t, mustMatcher(t, tpl, "a"), []matchCase{
		{"equal base paths are ambiguous", Record{Method: "GET", Host: "api.contoso.com", Path: "/v1/orders/1"}, "", ""},
		{"equal base paths, other template", Record{Method: "GET", Host: "api.contoso.com", Path: "/v1/carts/1"}, "", ""},
	})
}

func TestMatch_pin_service(t *testing.T) {
	m := mustMatcher(t, shop(), "orders")
	runMatchCases(t, m, []matchCase{
		{"pin, empty base path", Record{Method: "GET", Host: "unknown.example", Path: "/orders/42"}, "e1", "orders"},
		{"pin, server URL base path", Record{Method: "GET", Path: "/v1/orders/42"}, "e1", "orders"},
		{"record service wins over the pin", Record{Method: "GET", Path: "/invoices/7", Service: "billing"}, "e10", "billing"},
		{"record service with relative server URL", Record{Method: "GET", Path: "/api/invoices/7", Service: "billing"}, "e10", "billing"},
		{"unknown record service is unmatched", Record{Method: "GET", Path: "/orders/42", Service: "shipping"}, "", ""},
		{"host match comes before the pin", Record{Method: "GET", Host: "billing.internal", Path: "/orders/42"}, "", ""},
	})

	// A known host decides alone: a path under none of its base paths is unmatched, not handed to the pin.
	billingPin := mustMatcher(t, shop(), "billing")
	runMatchCases(t, billingPin, []matchCase{
		{"known host, no fitting base path, other pin", Record{Method: "GET", Host: "orders.internal", Path: "/invoices/7"}, "", ""},
		{"unknown host goes to the pin", Record{Method: "GET", Host: "elsewhere.internal", Path: "/invoices/7"}, "e10", "billing"},
	})

	if _, err := NewMatcher(shop(), "shipping"); err == nil {
		t.Error("NewMatcher with an unknown pinned service: want an error")
	}

	unpinned := mustMatcher(t, shop(), "")
	runMatchCases(t, unpinned, []matchCase{
		{"no pin, no host", Record{Method: "GET", Path: "/orders/42"}, "", ""},
	})
}

func TestMatch_prefers_literal_segments(t *testing.T) {
	m := mustMatcher(t, shop(), "orders")
	runMatchCases(t, m, []matchCase{
		{"literal over placeholder", Record{Method: "GET", Path: "/orders/export"}, "e2", "orders"},
		{"placeholder otherwise", Record{Method: "GET", Path: "/orders/exports"}, "e1", "orders"},
	})

	// Equal literal counts: the template whose first literal comes earlier wins, whatever the list order.
	tpl := Templates{Services: []Service{{ID: "s", Name: "svc", Endpoints: []Endpoint{
		{ID: "late", Method: "GET", Path: "/{}/items"},
		{ID: "early", Method: "GET", Path: "/orders/{}"},
	}}}}
	m = mustMatcher(t, tpl, "svc")
	runMatchCases(t, m, []matchCase{
		{"earlier literal wins a tie", Record{Method: "GET", Path: "/orders/items"}, "early", "svc"},
	})
}

func TestMatch_never_crosses_services(t *testing.T) {
	m := mustMatcher(t, shop(Mapping{Host: "api.contoso.com", ServiceID: "s1"}), "billing")
	runMatchCases(t, m, []matchCase{
		{"mapped service lacks the endpoint", Record{Method: "GET", Host: "api.contoso.com", Path: "/invoices/9"}, "", ""},
		{"host service lacks the endpoint", Record{Method: "GET", Host: "orders.internal", Path: "/v1/invoices/9"}, "", ""},
		{"record service lacks the endpoint", Record{Method: "GET", Path: "/invoices/9", Service: "orders"}, "", ""},
	})

	// A mapping to a service the templates do not contain matches nothing.
	m = mustMatcher(t, shop(Mapping{Host: "api.contoso.com", ServiceID: "gone"}), "billing")
	runMatchCases(t, m, []matchCase{
		{"mapping to unknown service", Record{Method: "GET", Host: "api.contoso.com", Path: "/invoices/9"}, "", ""},
	})
}

func TestMatch_api_mapping(t *testing.T) {
	m := mustMatcher(t, shop(
		Mapping{Host: "api.contoso.com", ServiceID: "s2"},
		Mapping{Host: "api.contoso.com", API: "orders-api", ServiceID: "s1"},
		Mapping{API: "legacy-orders", ServiceID: "s1", StripPrefix: "/legacy"},
	), "")
	runMatchCases(t, m, []matchCase{
		{"host and API beat host", Record{Method: "GET", Host: "api.contoso.com", API: "orders-api", Path: "/orders/5"}, "e1", "orders"},
		{"other API falls to host mapping", Record{Method: "GET", Host: "api.contoso.com", API: "billing-api", Path: "/invoices/5"}, "e10", "billing"},
		{"API only", Record{Method: "GET", Host: "gw.contoso.com", API: "legacy-orders", Path: "/legacy/orders/5"}, "e1", "orders"},
		{"API is case-sensitive", Record{Method: "GET", Host: "gw.contoso.com", API: "Legacy-Orders", Path: "/legacy/orders/5"}, "", ""},
		{"empty API never matches a set API", Record{Method: "GET", Host: "gw.contoso.com", Path: "/legacy/orders/5"}, "", ""},
	})
}
