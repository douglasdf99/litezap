package ipallow

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestParse(t *testing.T) {
	l, err := Parse([]string{"10.0.0.0/8", " 203.0.113.7 ", "2001:db8::/32", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 4 || l[1].String() != "203.0.113.7/32" || l[3].String() != "::1/128" {
		t.Fatalf("list = %v", l)
	}
	for _, bad := range []string{"", "10.0.0.0/33", "example.com", "10.0.0.1/8x", "*"} {
		if _, err := Parse([]string{bad}); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	// The network part is canonicalized (10.1.2.3/8 -> 10.0.0.0/8).
	if l, _ := Parse([]string{"10.1.2.3/8"}); l[0].String() != "10.0.0.0/8" {
		t.Errorf("masked = %v", l)
	}
	if len(l.Strings()) != 4 {
		t.Errorf("Strings = %v", l.Strings())
	}
}

func TestContains(t *testing.T) {
	l, _ := Parse([]string{"10.0.0.0/8", "2001:db8::/32"})
	for ip, want := range map[string]bool{
		"10.9.8.7": true, "11.0.0.1": false, "2001:db8::5": true, "2001:db9::5": false,
		"::ffff:10.1.1.1": true, // IPv4-mapped IPv6 matches the IPv4 entry
	} {
		if got := l.Contains(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Contains(%s) = %v", ip, got)
		}
	}
	if (List{}).Contains(netip.MustParseAddr("1.2.3.4")) {
		t.Error("empty list contains")
	}
	if l.Contains(netip.Addr{}) {
		t.Error("invalid address allowed")
	}
}

func TestClientIP(t *testing.T) {
	trusted, _ := Parse([]string{"10.0.0.0/8"})
	cases := []struct {
		remote, xff string
		trusted     List
		want        string
	}{
		{"203.0.113.9:5000", "", nil, "203.0.113.9"},
		// Without trusted proxies the header is ignored (a client could forge it).
		{"203.0.113.9:5000", "1.1.1.1", nil, "203.0.113.9"},
		// Not from a trusted proxy: ignored too.
		{"203.0.113.9:5000", "1.1.1.1", trusted, "203.0.113.9"},
		// From a trusted proxy: the rightmost untrusted entry.
		{"10.0.0.2:5000", "1.1.1.1", trusted, "1.1.1.1"},
		// A forged leftmost entry does not win over what the proxy appended.
		{"10.0.0.2:5000", "6.6.6.6, 198.51.100.4", trusted, "198.51.100.4"},
		// Chained trusted proxies are skipped.
		{"10.0.0.2:5000", "198.51.100.4, 10.0.0.3", trusted, "198.51.100.4"},
		// Several headers are one list, in order.
		{"10.0.0.2:5000", "198.51.100.4|10.0.0.9", trusted, "198.51.100.4"},
		// Garbage in the chain: stop and use the last good hop (the proxy itself).
		{"10.0.0.2:5000", "198.51.100.4, nonsense", trusted, "10.0.0.2"},
		// Everything trusted: the leftmost entry.
		{"10.0.0.2:5000", "10.0.0.5", trusted, "10.0.0.5"},
		{"[2001:db8::1]:443", "", nil, "2001:db8::1"},
		{"[::ffff:203.0.113.9]:1", "", nil, "203.0.113.9"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		for i, v := range splitBar(c.xff) {
			if i == 0 {
				r.Header.Set("X-Forwarded-For", v)
			} else {
				r.Header.Add("X-Forwarded-For", v)
			}
		}
		if got := ClientIP(r, c.trusted); got.String() != c.want {
			t.Errorf("ClientIP(%s, %q) = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func splitBar(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
