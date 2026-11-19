package usage

import "testing"

func TestSubjectCommonName(t *testing.T) {
	tests := []struct {
		value, cn string
		ok        bool
	}{
		// the RFC 2253 form
		{"CN=checkout,O=Acme", "checkout", true},
		{"CN=checkout", "checkout", true},
		{"cn=checkout,ou=Payments,o=Acme,l=Amsterdam,st=NH,c=NL", "checkout", true},
		{"SERIALNUMBER=7,STREET=Main St 1,CN=checkout", "checkout", true},
		{"CN = checkout, O = Acme", "checkout", true},        // spaces around the separators
		{"CN=checkout+OU=Payments,O=Acme", "checkout", true}, // a multi-valued part
		{"CN=Checkout Web,O=Acme", "Checkout Web", true},
		{"CN=jane@example.com,O=Acme", "jane@example.com", true}, // the guard withholds it afterwards
		{"O=Acme,CN=", "", false},
		// escapes: a backslash before a special character
		{`CN=checkout,O=Acme\, Inc.`, "checkout", true},
		{`O=Acme\, Inc.,CN=checkout`, "checkout", true},
		{`CN=check\,out,O=Acme`, "check,out", true},
		{`CN=a\+b\=c\\d,O=Acme`, `a+b=c\d`, true},
		{`CN=\#1 app,O=Acme`, "#1 app", true},
		{`CN=app\ ,O=Acme`, "", false},     // an escaped space at the end is not read
		{`CN=caf\C3\A9,O=Acme`, "", false}, // a hexadecimal pair
		{`CN=checkout\n,O=Acme`, "", false},
		{`CN=checkout,O=Acme\`, "", false},
		{`CN="checkout",O=Acme`, "", false}, // a quoted value
		{`CN=checkout,O="Acme, Inc."`, "", false},
		{"CN=#04024869,O=Acme", "", false}, // a hexadecimal value
		{"CN=a=b,O=Acme", "", false},       // a bare "="
		{"CN=a;O=Acme", "", false},         // the ";" separator of RFC 1779
		{"CN=<a>,O=Acme", "", false},
		// the legacy form
		{"/C=NL/O=Acme/CN=checkout", "checkout", true},
		{"/CN=checkout", "checkout", true},
		{"/O=Acme, Inc./CN=checkout", "checkout", true},
		{"/C=NL/O=Acme", "", false},
		{"/CN=check/out/O=Acme", "", false}, // "out" is not TYPE=value
		{"/CN=checkout/", "", false},
		{`/CN=check\/out/O=Acme`, "", false},
		{`/CN=check\out/O=Acme`, "", false}, // a backslash in the legacy form
		{"/CN=a=b/O=Acme", "", false},
		{"/v1/orders/8812", "", false},
		{"/", "", false},
		// not a subject, or not one this reads
		{"OU=Payments,O=Acme", "", false},      // no common name
		{"CN=svc,CN=Users,DC=acme", "", false}, // two common names
		// a subject with a part that may name a person (a user's directory entry, a personal certificate) is not read
		{"cn=John Smith,ou=people,dc=example,dc=com", "", false},
		{"/C=US/O=Acme/CN=John Smith/emailAddress=j@x.com", "", false},
		{"UID=jsmith,CN=John Smith", "", false},
		{"CN=Jane Doe,emailAddress=jane@contoso.com", "", false},
		{"E=ops@acme.example,CN=checkout", "", false},
		{"emailAddress=ops@acme.example,CN=checkout,DC=acme,DC=example", "", false},
		{"/CN=checkout/emailAddress=ops@acme.example", "", false},
		{"CN=checkout,uid=42", "", false},
		{"CN=checkout+E=ops@acme.example", "", false},
		{"CN=checkout,title=Engineer", "", false}, // a type that is not in the list
		{"CN=checkout,2.5.4.10=Acme", "", false},  // an OID as the type
		{"CN=checkout,Acme", "", false},           // a part that is not TYPE=value
		{"CN=checkout,", "", false},
		{"a=b", "", false},
		{"page=2&token=abc", "", false},
		{"cn=checkout&o=acme", "", false}, // a query string: "=" inside the value
		{`{"card":"4111"}`, "", false},
		{"checkout-web", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		cn, ok := subjectCommonName(tt.value)
		if ok != tt.ok || ok && cn != tt.cn {
			t.Errorf("subjectCommonName(%q) = %q, %v, want %q, %v", tt.value, cn, ok, tt.cn, tt.ok)
		}
	}
}
