package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/graphwake/agent/internal/ci"
)

func TestWarnFormatPerPlatform(t *testing.T) {
	tests := []struct {
		platform ci.Platform
		want     string
	}{
		{ci.GitHub, "::warning::graphwake: spec rejected: 100%25 bad\n"},
		{ci.Azure, "##vso[task.logissue type=warning]graphwake: spec rejected: 100%AZP25 bad\n"},
		{ci.GitLab, "WARNING graphwake: spec rejected: 100% bad\n"},
		{ci.Jenkins, "WARNING graphwake: spec rejected: 100% bad\n"},
		{ci.Other, "WARNING graphwake: spec rejected: 100% bad\n"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		r := &Reporter{W: &out, Platform: tt.platform}
		r.Warn("spec rejected: %s", "100% bad")
		if out.String() != tt.want {
			t.Errorf("%s: %q, want %q", tt.platform, out.String(), tt.want)
		}
	}
}

func TestOneLineAndSecretsHidden(t *testing.T) {
	var out bytes.Buffer
	r := &Reporter{W: &out, Platform: ci.GitHub}
	r.Hide("dsk_secret")
	r.Hide("  ")
	r.Warn("spec rejected: line1\nline2 dsk_secret\r\n::error::x")
	r.Info("sent with dsk_secret")
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %q", out.String())
	}
	if strings.Contains(out.String(), "dsk_secret") {
		t.Fatalf("secret printed: %q", out.String())
	}
	if lines[0] != "::warning::graphwake: spec rejected: line1 line2 *** ::error::x" {
		t.Fatalf("line = %q", lines[0])
	}
}

func TestLongMessagesAreTruncated(t *testing.T) {
	var out bytes.Buffer
	r := &Reporter{W: &out, Platform: ci.Other}
	r.Info("%s", strings.Repeat("é", 2000))
	if n := len([]rune(out.String())); n > maxLen+len(prefix)+5 {
		t.Fatalf("line has %d runes", n)
	}
}
