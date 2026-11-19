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
	hexPattern   = regexp.MustCompile(`^[0-9A-Fa-f]+$`)
	hexRun       = regexp.MustCompile(`[0-9A-Fa-f]{32,}`)
)

// LooksPersonal reports whether a caller name matches one of the withholding rules. Such a name is never sent; its
// requests are only counted as withheld. The rules, each stated with examples in the README ("What the usage step
// sends") and mirrored rule for rule by the server's guard, withhold a name that:
//   - contains an e-mail address;
//   - contains an IPv4 or IPv6 literal (see hasIPLiteral);
//   - contains a run of 32 or more hexadecimal characters, also behind a label ("token-<hex>");
//   - contains a secret-like run of 32 or more characters of [A-Za-z0-9+/_=-] that is not exactly a canonical UUID
//     and is pure hexadecimal or mixes upper case, lower case and digits (see looksRandom);
//   - contains a control character (newline, carriage return, tab, ESC, ...) or "##": no real client or service name
//     has one, and such a name could break a log line or start a CI logging command ("##vso[") wherever it is
//     printed;
//   - looks like a path or a URL (see looksLikePath): a caller field pointed at a request path must not send that
//     path or its query string;
//   - looks like structured content (see looksStructured): a caller field pointed at a body or a query string must
//     not send it.
//
// The rules are a safety net for common shapes of personal or secret values, not a guarantee.
func LooksPersonal(name string) bool {
	if strings.Contains(name, "##") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return true
	}
	if looksLikePath(name) || looksStructured(name) {
		return true
	}
	if emailPattern.MatchString(name) || hexRun.MatchString(name) {
		return true
	}
	for _, tok := range tokenPattern.FindAllString(name, -1) {
		if !uuidPattern.MatchString(tok) && looksRandom(tok) {
			return true
		}
	}
	return hasIPLiteral(name)
}

// looksLikePath reports whether a caller name looks like a path or a URL: the trimmed name starts with "/", or
// contains "://" or "?", or any of its whitespace-separated words starts with "/" and is longer than one character
// (a method-prefixed path such as "GET /v1/orders/8812"). "team/app" and "Checkout / Web" are not one.
func looksLikePath(name string) bool {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "/") || strings.Contains(name, "://") || strings.Contains(name, "?") {
		return true
	}
	for _, word := range strings.Fields(name) {
		if len(word) > 1 && strings.HasPrefix(word, "/") {
			return true
		}
	}
	return false
}

// structuredChars are the characters of JSON and XML text and of key=value pairs. "&" is not one: names such as
// "R&D" have it.
const structuredChars = `{}"<>=`

// looksStructured reports whether a caller name looks like structured content (a JSON or XML body, a key=value query
// string): it contains any of { } " < > =.
func looksStructured(name string) bool {
	return strings.ContainsAny(name, structuredChars)
}

// looksRandom tells a secret from a long service name: a secret is pure hexadecimal or mixes upper-case letters,
// lower-case letters and digits; "checkout-web-service-production-eu-west-1" is neither.
func looksRandom(tok string) bool {
	return hexPattern.MatchString(tok) ||
		strings.ContainsAny(tok, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") &&
			strings.ContainsAny(tok, "abcdefghijklmnopqrstuvwxyz") &&
			strings.ContainsAny(tok, "0123456789")
}

// hasIPLiteral splits the name on everything but letters, digits, '.' and ':' and checks each whole piece: an IPv6
// address, or four dot-separated numbers anywhere in it that form an IPv4 address ("10.0.0.12:8080",
// "10.0.0.12.nip.io"). So "orders::api" or "team:app:prod" are not taken for IPv6.
func hasIPLiteral(name string) bool {
	pieces := strings.FieldsFunc(name, func(r rune) bool {
		return !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) || r == '.' || r == ':')
	})
	for _, p := range pieces {
		p = strings.Trim(p, ".")
		if strings.Contains(p, ":") && strings.ContainsAny(p, "0123456789abcdefABCDEF") {
			if addr, err := netip.ParseAddr(p); err == nil && addr.Is6() {
				return true
			}
		}
		for _, part := range strings.Split(p, ":") {
			if hasIPv4(strings.Split(part, ".")) {
				return true
			}
		}
	}
	return false
}

// hasIPv4 reports whether four consecutive labels form an IPv4 address.
func hasIPv4(labels []string) bool {
	for i := 0; i+4 <= len(labels); i++ {
		if addr, err := netip.ParseAddr(strings.Join(labels[i:i+4], ".")); err == nil && addr.Is4() {
			return true
		}
	}
	return false
}
