package usage

import "strings"

// subjectTypes are the attribute types a certificate subject may have for subjectCommonName to read it, in lower
// case. UID, DC, emailAddress and E are not among them: a subject with one of those may be a person's (a user's
// directory entry, a personal certificate), whose common name is the person's name, so it is not reduced; it keeps
// its "=" and the guard withholds it.
var subjectTypes = map[string]bool{
	"cn": true, "o": true, "ou": true, "c": true, "l": true, "st": true, "street": true, "serialnumber": true,
}

// subjectCommonName returns the common name of a certificate subject, the value NGINX's $ssl_client_s_dn holds for
// mutual TLS. ok is true only when the whole value is a subject: in the comma-separated form of RFC 2253
// ("CN=checkout,O=Acme") or in the legacy form that starts with "/" ("/O=Acme/CN=checkout"), every part is
// TYPE=value with a type of subjectTypes (letter case ignored), and exactly one part is a common name that is not
// empty. Anything else is not read as a subject (ok is false): "a=b", "page=2&token=abc", a subject with a UID, DC,
// emailAddress or E part, a subject without a common name or with two, a quoted or hexadecimal value, an escape
// other than a backslash before one of , + " \ < > ; = # or a space.
func subjectCommonName(value string) (cn string, ok bool) {
	legacy := strings.HasPrefix(value, "/")
	var parts []string
	if legacy {
		if strings.Contains(value, `\`) {
			return "", false
		}
		parts = strings.Split(value[1:], "/")
	} else if parts, ok = splitSubject(value); !ok {
		return "", false
	}
	found := false
	for _, part := range parts {
		typ, val, isPair := strings.Cut(part, "=")
		typ = strings.ToLower(strings.TrimSpace(typ))
		if !isPair || !subjectTypes[typ] {
			return "", false
		}
		if val, ok = subjectValue(strings.TrimSpace(val), legacy); !ok {
			return "", false
		}
		if typ == "cn" {
			if found {
				return "", false
			}
			cn, found = strings.TrimSpace(val), true
		}
	}
	return cn, found && cn != ""
}

// splitSubject splits an RFC 2253 subject at every "," and "+" that no backslash escapes; the escapes stay in the
// parts. ok is false when the value ends in a lone backslash.
func splitSubject(value string) (parts []string, ok bool) {
	start := 0
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			if i++; i == len(value) {
				return nil, false
			}
		case ',', '+':
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	return append(parts, value[start:]), true
}

// subjectValue returns one attribute value without its escapes. ok is false for a value that cannot be read with
// confidence: one with a bare = " ; < or >, one that starts with "#" (a hexadecimal value) and, in the RFC 2253
// form, an escape other than a backslash before one of , + " \ < > ; = # or a space (such as a hexadecimal pair) and
// an escaped space at the very end.
// The legacy form has no escapes; a value with a backslash never gets here.
func subjectValue(val string, legacy bool) (string, bool) {
	if strings.HasPrefix(val, "#") {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(val); i++ {
		c := val[i]
		switch {
		case c == '\\' && !legacy:
			if i++; i == len(val) || !strings.ContainsRune(`,+"\<>;=# `, rune(val[i])) {
				return "", false
			}
			c = val[i]
		case strings.ContainsRune(`="<>;`, rune(c)):
			return "", false
		}
		b.WriteByte(c)
	}
	return b.String(), true
}
