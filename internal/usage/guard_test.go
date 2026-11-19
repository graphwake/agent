package usage

import "testing"

func TestLooksPersonal(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// control characters and "##" (a CI logging command, a broken log line); a single "#" is fine
		{"x ##vso[task.setvariable variable=a]b", true},
		{"team##name", true},
		{"a\nb", true},
		{"a\x1bb", true},
		{"a\tb", true},
		{"a\rb", true},
		{"C# service", false},
		{"issue #12 client", false},
		// e-mail addresses
		{"a@b.co", true},
		{"jane.doe@contoso.com", true},
		{"user jane@contoso.com (prod)", true},
		// IPv4 and IPv6 literals
		{"10.0.0.12", true},
		{"client 192.168.1.20:8080", true},
		{"::1", true},
		{"2001:db8::8a2e:370:7334", true},
		{"[fe80::1]:443", true},
		// secret-like strings: 32+ characters, pure hex or mixed upper case, lower case and digits
		{"eyJhbGciOiJIUzI1NiJ9eyJzdWIiOiIxMjM0NTY3ODkwIn0", true},
		{"0123456789abcdef0123456789abcdef", true},
		{"da39a3ee5e6b4b0d3255bfef95601890afd80709", true},    // 40-character lower-case hex
		{"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk", true}, // 43-character base64url
		// a run of 32+ hex characters behind a label
		{"apikey=0123456789abcdef0123456789abcdef", true},
		{"token-da39a3ee5e6b4b0d3255bfef95601890afd80709", true},
		{"svc_0123456789abcdef0123456789abcdef", true},
		// an IPv4 literal with a dotted suffix
		{"client 10.0.0.12.nip.io", true},
		// long names that do not look random (no upper case, not pure hex)
		{"checkout-web-service-production-eu-west-1", false},
		{"orders-fulfilment-service-production-api", false},
		{"550e8400-e29b-41d4-a716-446655440000-extra", false},
		// paths and URLs: a name that starts with "/", or contains "://" or "?"
		{"/v1/orders/8812?token=abc", true},
		{"/orders", true},
		{"  /orders", true},
		{"https://orders.example/a", true},
		{"orders?page=2", true},
		{"team/app", false},
		{"a/b/c", false},
		// a word that starts with "/" and is longer than "/" (a method-prefixed path, such as a resource name)
		{"GET /v1/orders/8812", true},
		{"POST /orders", true},
		{"orders /v1/x", true},
		{"Checkout / Web", false},
		// structured content: any of { } " < > = (a JSON or XML body, a key=value query string); "&" is allowed
		{`{"card":"4111"}`, true},
		{"page=2&token=abc", true},
		{`<order id="1">`, true},
		{"a=b", true},
		{"R&D team", false},
		// ordinary caller names
		{"checkout-web", false},
		{"master", false},
		{"5f1d7c2e9a3b4c6d8e0f1a2b", false}, // 24-character hex id
		{"550e8400-e29b-41d4-a716-446655440000", false},
		{"550E8400-E29B-41D4-A716-446655440000", false},
		{"Checkout Web (prod)", false},
		{"orders::api", false},
		{"team:app:prod", false},
		{"billing-service v2.1.0", false},
		{"", false},
		// each rule on its own: a name here trips one rule only, so removing that rule fails the row
		{"orders?page", true}, // "?"
		{"/", true},           // starts with "/" (the word rule needs more than "/")
		{"a ://b", true},      // "://"
		{"{a", true},
		{"a}", true},
		{"<a", true},
		{"a>b", true},
		{`a"b`, true},
		{"a\u0085b", true}, // a control character that is not ASCII
		// e-mail addresses: "@", then groups of ASCII letters, digits and hyphens joined by single dots, where a
		// group after the first begins with two or more letters
		{"jane@x.co1", true},
		{"jane@localhost", false},        // no dot in the domain
		{"jane@contoso.c", false},        // one letter after the dot
		{"jane@contoso.c0m", false},      // one letter after the dot
		{"jan\u00e9@contoso.com", false}, // a letter that is not ASCII before the "@"
		{"jane @contoso.com", false},
		// IPv4: four whole dot-separated numbers, 0 to 255, no leading zero
		{"src:10.0.0.12", true},
		{"host.10.0.0.12", true},
		{"client-10.0.0.12", true},
		{"1.2.3.4", true}, // a four-part version number is one too
		{"ip10.0.0.12", false},
		{"10.0.0.12abc", false},
		{"010.0.0.12", false},
		{"10.0.0.256", false},
		{"ip-10-0-0-12", false},
		// IPv6: a whole piece of the name that has a hexadecimal digit
		{"client 2001:db8::1", true},
		{"fe80::1%eth0", true},
		{"src:2001:db8::1", false},
		{"2001:db8::1.nip.io", false},
		{"::", false},
		{"a :: b", false},
		// secret-like runs: 32 characters at least, with upper case, lower case and a digit, and not a UUID
		{"Abcdefghijklmnopqrstuvwxyz012345", true},            // 32 characters
		{"Abcdefghijklmnopqrstuvwxyz01234", false},            // 31 characters
		{"tok_abcdefghijklmnopqrstuvwxyz0123456789ab", false}, // no upper case
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789AB", false},     // no lower case
		{"AbcdefghijklmnopqrstuvwxyzAbcdefghij", false},       // no digit
		{"550e8400-E29B-41d4-a716-446655440000", false},       // a UUID in mixed case
		{"550e8400-E29B-41d4-a716-446655440000-extra", true},  // more than a UUID
		{"0123456789abcdef0123456789abcde", false},            // 31 hexadecimal characters
		// values no rule catches: the rules are a safety net, not a guarantee
		{"Jane Doe", false},
		{"hunter2", false},
		{"v1/orders/8812", false},
		{"uri:/v1/x", false},
		{"card:4111,cvc:123", false},
		// the edges of the rules: dots at the ends of a piece are dropped, a letter that is not ASCII ends a piece,
		// white space before a lone "/" is not part of the name
		{"host 2001:db8::1.", true},
		{"\u00e910.0.0.12", true},
		{" /", true},
	}
	for _, tt := range tests {
		if got := LooksPersonal(tt.name); got != tt.want {
			t.Errorf("LooksPersonal(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
