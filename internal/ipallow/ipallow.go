// Package ipallow holds IP allowlists (lists of addresses and CIDR ranges) and the
// resolution of a request's client address behind trusted reverse proxies.
package ipallow

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// List is a set of address ranges. The zero value is empty (it allows nothing; callers
// treat an empty list as "no restriction").
type List []netip.Prefix

// MaxEntries bounds a list (stored per instance and per key).
const MaxEntries = 100

// Parse accepts single addresses ("203.0.113.7", "::1") and CIDR ranges ("10.0.0.0/8").
func Parse(entries []string) (List, error) {
	if len(entries) > MaxEntries {
		return nil, fmt.Errorf("at most %d entries", MaxEntries)
	}
	out := make(List, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if strings.Contains(e, "/") {
			p, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", e)
			}
			out = append(out, normalize(p))
			continue
		}
		a, err := netip.ParseAddr(e)
		if err != nil {
			return nil, fmt.Errorf("invalid IP address %q", e)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// normalize masks the host bits and turns an IPv4-mapped range into plain IPv4.
func normalize(p netip.Prefix) netip.Prefix {
	if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
		p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
	}
	return p.Masked()
}

// Strings renders the list (canonical CIDR form).
func (l List) Strings() []string {
	out := make([]string, len(l))
	for i, p := range l {
		out[i] = p.String()
	}
	return out
}

// Contains reports whether a is in one of the ranges.
func (l List) Contains(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	for _, p := range l {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP is the address of the client that made r. X-Forwarded-For is read only when
// the connection comes from a trusted proxy: the header is walked from the right (the
// entry appended by the nearest proxy) past every trusted hop, and the first untrusted
// address is the client. A client cannot spoof its address by sending the header itself,
// because entries left of the first untrusted hop are never read.
func ClientIP(r *http.Request, trusted List) netip.Addr {
	remote := parseHostPort(r.RemoteAddr)
	if len(trusted) == 0 || !trusted.Contains(remote) {
		return remote
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(h, ",") {
			hops = append(hops, strings.TrimSpace(part))
		}
	}
	client := remote
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			return client // malformed chain: trust nothing further left
		}
		client = a.Unmap()
		if !trusted.Contains(client) {
			return client
		}
	}
	return client
}

func parseHostPort(addr string) netip.Addr {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}
