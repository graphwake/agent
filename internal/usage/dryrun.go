package usage

import (
	"fmt"
	"io"
)

// RenderDryRun prints, for --dry-run, the exact body that would be sent, then what stays on this machine: the
// unmatched requests by host and path prefix, and the withheld and anonymous request counts.
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
			host := p.Host
			if host == "" {
				host = "(no host)"
			}
			fmt.Fprintf(w, "    %s %s  %d\n", host, p.Prefix, p.Count)
		}
	}
	fmt.Fprintf(w, "  Requests withheld because the caller name looked personal: %d\n", withheld)
	fmt.Fprintf(w, "  Requests without a caller name: %d\n", anonymous)
}
