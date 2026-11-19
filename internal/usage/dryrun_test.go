package usage

import (
	"bytes"
	"strings"
	"testing"
)

func TestDryRun_prints_body_verbatim(t *testing.T) {
	body := []byte(`{"preset":"apim","rows":[{"caller":"checkout-web","path":"/orders/{}"}]}`)
	unmatched := []PrefixCount{
		{Host: "api.contoso.com", Prefix: "/legacy/v2", Count: 412},
		{Host: "", Prefix: "/health", Count: 7},
	}
	var out bytes.Buffer
	RenderDryRun(&out, body, unmatched, 3, 9)
	got := out.String()

	sent := "--- would send ---\n" + string(body) + "\n--- end ---\n"
	if !strings.Contains(got, sent) {
		t.Fatalf("output does not hold the body verbatim between the markers:\n%s", got)
	}
	_, rest, ok := strings.Cut(got, sent)
	if !ok {
		t.Fatal("unreachable")
	}
	if !strings.HasPrefix(rest, "Not sent (stays on this machine):\n") {
		t.Fatalf("after the body, want the not-sent section; got:\n%s", rest)
	}
	for _, want := range []string{"api.contoso.com /legacy/v2", "412", "(no host) /health", "withheld because the caller name looked personal: 3", "without a caller name: 9"} {
		if !strings.Contains(rest, want) {
			t.Errorf("not-sent section lacks %q:\n%s", want, rest)
		}
	}
	if strings.Index(rest, "/legacy/v2") > strings.Index(rest, "/health") {
		t.Errorf("unmatched prefixes not in the given order:\n%s", rest)
	}

	out.Reset()
	RenderDryRun(&out, body, nil, 0, 0)
	if !strings.Contains(out.String(), "matched no endpoint: none") {
		t.Errorf("empty unmatched table:\n%s", out.String())
	}
}

// Hosts and prefixes come from the input: none of them can start a new line, move the cursor or start a CI command.
func TestDryRun_escapes_unsent_values(t *testing.T) {
	unmatched := []PrefixCount{
		{Host: "evil\n::error::host", Prefix: "/a\n::error::x", Count: 1},
		{Host: "h\r##vso[task.logissue]x", Prefix: "/b\n##vso[task.logissue]x", Count: 2},
		{Host: "\x1b[31mred", Prefix: "/c\x1b]0;title\x07/d\r", Count: 3},
		{Host: "sep\u2028x", Prefix: "/bad\xffutf8/##[error]x", Count: 4},
	}
	var out bytes.Buffer
	RenderDryRun(&out, []byte(`{}`), unmatched, 0, 0)
	got := out.String()
	if strings.ContainsAny(got, "\r\x1b\x07\u2028") || strings.Contains(got, "\xff") {
		t.Fatalf("raw control character in output: %q", got)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 11 { // 3 body lines, section header, table header, 4 rows, withheld, anonymous
		t.Fatalf("%d lines, want 11:\n%s", len(lines), got)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "::") || strings.Contains(line, "##") {
			t.Errorf("line can start a CI command: %q", line)
		}
	}
	for _, want := range []string{
		`    evil\x0a::error::host /a\x0a::error::x  1`,
		`    h\x0d#\x23vso[task.logissue]x /b\x0a#\x23vso[task.logissue]x  2`,
		`    \x1b[31mred /c\x1b]0;title\x07/d\x0d  3`,
		`    sep\u2028x /bad\xffutf8/#\x23[error]x  4`,
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

// The would-send body is JSON: Encode escapes every control character in names and paths, so it is one safe line.
func TestDryRun_body_escapes_control_characters(t *testing.T) {
	one := int64(1)
	body, err := Encode(Snapshot{Rows: []Row{{Caller: "a\n::error::x\r\x1b[31m\u2028", Service: "s\n##vso[task.logissue]x",
		Path: "/p\n", Count: &one}}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	RenderDryRun(&out, body, nil, 0, 0)
	if strings.ContainsAny(string(body), "\n\r\x1b\u2028") || !strings.HasPrefix(out.String(), "--- would send ---\n"+string(body)+"\n--- end ---\n") {
		t.Fatalf("body not one escaped line: %q", out.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "::") || strings.HasPrefix(line, "##") {
			t.Errorf("line starts a CI command: %q", line)
		}
	}
}
