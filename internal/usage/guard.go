package usage

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode"
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	tokenPattern = regexp.MustCompile(`[A-Za-z0-9+/_=\-]{32,}`)
	uuidPattern  = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
)

// LooksPersonal reports whether a caller name may identify a person or a secret rather than an application: it holds
// an e-mail address, an IPv4 or IPv6 literal, or a token-like run of 32 or more characters of [A-Za-z0-9+/_=-]
// that is not a canonical UUID. Such names are never sent; their requests are only counted as withheld.
func LooksPersonal(name string) bool {
	if emailPattern.MatchString(name) {
		return true
	}
	for _, tok := range tokenPattern.FindAllString(name, -1) {
		if !uuidPattern.MatchString(tok) {
			return true
		}
	}
	return hasIPLiteral(name)
}

// hasIPLiteral splits the name on everything but letters, digits, '.' and ':' and parses each whole piece as an
// address (or address:port), so "orders::api" or "team:app:prod" are not taken for IPv6.
func hasIPLiteral(name string) bool {
	pieces := strings.FieldsFunc(name, func(r rune) bool {
		return !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) || r == '.' || r == ':')
	})
	for _, p := range pieces {
		p = strings.Trim(p, ".")
		if !strings.ContainsAny(p, ".:") || !strings.ContainsAny(p, "0123456789abcdefABCDEF") {
			continue
		}
		if _, err := netip.ParseAddr(p); err == nil {
			return true
		}
		if _, err := netip.ParseAddrPort(p); err == nil {
			return true
		}
	}
	return false
}
