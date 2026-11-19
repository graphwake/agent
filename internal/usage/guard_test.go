package usage

import "testing"

func TestLooksPersonal(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
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
	}
	for _, tt := range tests {
		if got := LooksPersonal(tt.name); got != tt.want {
			t.Errorf("LooksPersonal(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
