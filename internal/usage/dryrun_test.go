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
