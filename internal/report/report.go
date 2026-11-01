// Package report writes the agent's single output line in the CI platform's format, with secrets removed.
package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/graphwake/agent/internal/ci"
)

const prefix = "graphwake: "

// maxLen keeps a line readable when the server sends a long error text.
const maxLen = 500

// Reporter prints one line per call. Secrets registered with Hide never appear in its output.
type Reporter struct {
	W        io.Writer
	Platform ci.Platform
	secrets  []string
}

// Hide registers a value (the ingest key, a token) that must never be printed.
func (r *Reporter) Hide(secret string) {
	if strings.TrimSpace(secret) != "" {
		r.secrets = append(r.secrets, secret)
	}
}

// Info prints a plain line (success, or "nothing sent" explanations).
func (r *Reporter) Info(format string, args ...any) {
	fmt.Fprintln(r.W, prefix+r.clean(fmt.Sprintf(format, args...)))
}

// Warn prints a warning in the platform's warning format.
func (r *Reporter) Warn(format string, args ...any) {
	msg := prefix + r.clean(fmt.Sprintf(format, args...))
	switch r.Platform {
	case ci.GitHub:
		msg = "::warning::" + escapeGitHub(msg)
	case ci.Azure:
		msg = "##vso[task.logissue type=warning]" + strings.ReplaceAll(msg, "%", "%AZP25")
	default:
		msg = "WARNING " + msg
	}
	fmt.Fprintln(r.W, msg)
}

func (r *Reporter) clean(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	s = strings.Join(strings.Fields(s), " ") // one line, no control characters from server texts
	if runes := []rune(s); len(runes) > maxLen {
		s = string(runes[:maxLen]) + "..."
	}
	return s
}

func escapeGitHub(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}
