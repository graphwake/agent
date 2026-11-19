package usage

import (
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RenderDryRun prints, for --dry-run, the exact body that would be sent, then what stays on this machine: the
// unmatched requests by host and path prefix, and the withheld and anonymous request counts. The body is JSON, whose
// encoding escapes every control character; hosts and prefixes come from the input and are printed through visible,
// so no input can start a new output line or reach the terminal or the CI log as a command.
func RenderDryRun(w io.Writer, body []byte, unmatched []PrefixCount, withheld, anonymous int64) {
	fmt.Fprintln(w, "--- would send ---")
	fmt.Fprintln(w, string(body))
	fmt.Fprintln(w, "--- end ---")
	fmt.Fprintln(w, "Not sent (stays on this machine):")
	if len(unmatched) == 0 {
		fmt.Fprintln(w, "  Requests that matched no endpoint: none")
	} else {
		fmt.Fprintln(w, "  Requests that matched no endpoint, by host and path prefix:")
		for _, p := range unmatched {
			if p.Other {
				fmt.Fprintf(w, "    (other)  %d\n", p.Count)
				continue
			}
			host := p.Host
			if host == "" {
				host = "(no host)"
			}
			fmt.Fprintf(w, "    %s %s  %d\n", visible(host), visible(p.Prefix), p.Count)
		}
	}
	fmt.Fprintf(w, "  Requests withheld because the caller name looked personal: %d\n", withheld)
	fmt.Fprintf(w, "  Requests without a caller name: %d\n", anonymous)
}

// visible makes a value from the input safe to print on one line: every rune that is not printable (control
// characters such as a newline, a carriage return or ESC, line separators, invalid UTF-8) is written as a \x, \u or
// \U escape, and "##" becomes "#\x23" so no Azure Pipelines logging command ("##vso[", "##[") can start anywhere in
// the line.
func visible(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, "\\x%02x", s[i])
		case r == '#' && strings.HasPrefix(s[i+size:], "#"):
			b.WriteString("#\\x23")
			size++
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, "\\x%02x", r)
		case r < 0x10000:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			fmt.Fprintf(&b, "\\U%08x", r)
		}
		i += size
	}
	return b.String()
}
