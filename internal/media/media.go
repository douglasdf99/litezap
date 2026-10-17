// Package media fetches attachments for sending (media.url with SSRF protection, or
// base64) and signs temporary download URLs for received media (spec 4.1 and 4.5).
package media

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrTooLarge = errors.New("media exceeds the size limit")
	// ErrFetch means media.url could not be downloaded (network, HTTP status, bad URL).
	ErrFetch   = errors.New("media could not be fetched")
	ErrBlocked = errors.New("media URL points to a private or local address")
)

// MaxSize is the send limit per message type (spec 4.5).
func MaxSize(kind string) int64 {
	switch kind {
	case "image":
		return 16 << 20
	case "video", "audio":
		return 64 << 20
	default:
		return 100 << 20
	}
}

// Fetcher downloads media.url attachments.
type Fetcher struct {
	// AllowPrivate disables the address check (local installs).
	AllowPrivate bool
	Timeout      time.Duration // default 30s
	// LookupIP resolves host names (default: net.DefaultResolver). Tests replace it.
	LookupIP func(ctx context.Context, host string) ([]netip.Addr, error)
	// allow decides whether host may be reached at ip (default: publicAddr). Tests replace it.
	allow      func(host string, ip netip.Addr) bool
	clientOnce sync.Once
	client     *http.Client
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64: can map to private IPv4
	netip.MustParsePrefix("2002::/16"),     // 6to4: embeds any IPv4
}

// publicAddr reports whether ip is a public unicast address a user-supplied URL may reach.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// dial resolves the host itself and checks every address before connecting, for every
// connection (redirects included): a name cannot resolve to a blocked address (DNS
// rebinding), and a public URL cannot redirect to an internal one.
func (f *Fetcher) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		lookup := f.LookupIP
		if lookup == nil {
			lookup = func(ctx context.Context, h string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
			}
		}
		if ips, err = lookup(ctx, host); err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no address for %s", host)
	}
	allow := f.allow
	if allow == nil {
		allow = func(_ string, ip netip.Addr) bool { return f.AllowPrivate || publicAddr(ip) }
	}
	for _, ip := range ips {
		if !allow(host, ip) {
			return nil, ErrBlocked
		}
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, netip.AddrPortFrom(ips[0], uint16(p)).String())
}

func (f *Fetcher) httpClient() *http.Client {
	f.clientOnce.Do(f.initClient)
	return f.client
}

func (f *Fetcher) initClient() {
	f.client = &http.Client{
		Transport: &http.Transport{
			Proxy:               nil, // never via an environment proxy: dial must see the real target
			DialContext:         f.dial,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to a non-http URL")
			}
			return nil
		},
	}
}

// Fetch downloads rawURL, refusing bodies larger than max. It returns the data and the
// content type (from the response, or sniffed).
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, max int64) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("%w: media.url must be an http(s) URL", ErrFetch)
	}
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrFetch, err)
	}
	resp, err := f.httpClient().Do(req)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			return nil, "", ErrBlocked
		}
		return nil, "", fmt.Errorf("%w: %v", ErrFetch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("%w: HTTP %d", ErrFetch, resp.StatusCode)
	}
	if resp.ContentLength > max {
		return nil, "", ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrFetch, err)
	}
	if int64(len(data)) > max {
		return nil, "", ErrTooLarge
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func baseType(mime string) string {
	m, _, _ := strings.Cut(mime, ";")
	return strings.ToLower(strings.TrimSpace(m))
}

func fits(kind, mime string) bool {
	switch kind {
	case "image":
		return strings.HasPrefix(mime, "image/") && mime != "image/svg+xml"
	case "video":
		return strings.HasPrefix(mime, "video/")
	case "audio":
		return strings.HasPrefix(mime, "audio/")
	}
	return mime != ""
}

// notMedia reports sniffed types that are certainly not audio or video.
func notMedia(sniffed string) bool {
	switch {
	case strings.HasPrefix(sniffed, "text/"), strings.HasPrefix(sniffed, "application/x-"):
		return true // text, HTML, XML/SVG, scripts, archives, executables
	}
	switch sniffed {
	case "application/pdf", "application/zip", "image/svg+xml":
		return true
	}
	return false
}

// ResolveMime picks the attachment's MIME type: the one the client declared if it fits the
// message type, else the server's Content-Type if it fits, else what the bytes look like.
// The bytes are always checked first: an image must sniff as an image (never SVG), and
// audio/video must not sniff as text, documents, archives or executables, whatever type
// was declared. A remote Content-Type that contradicts the message type is ignored.
func ResolveMime(kind, declared, remote string, data []byte) (string, error) {
	sniffed := baseType(http.DetectContentType(data))
	if kind == "audio" && sniffed == "application/ogg" {
		sniffed = "audio/ogg"
	}
	bad := fmt.Errorf("media content is not a valid %s", kind)
	switch kind {
	case "image":
		if !fits(kind, sniffed) {
			return "", bad
		}
	case "video", "audio":
		if notMedia(sniffed) {
			return "", bad
		}
	}
	for _, m := range []string{declared, remote} {
		if b := baseType(m); b != "" && b != "application/octet-stream" && fits(kind, b) {
			return b, nil
		}
	}
	if fits(kind, sniffed) {
		return sniffed, nil
	}
	if kind == "document" {
		return "application/octet-stream", nil
	}
	return "", bad
}

// DecodeBase64 decodes media.base64 (plain or as a data: URL, whose type is returned),
// refusing more than max bytes.
func DecodeBase64(s string, max int64) (data []byte, mime string, err error) {
	if rest, ok := strings.CutPrefix(s, "data:"); ok {
		meta, payload, found := strings.Cut(rest, ",")
		if !found || !strings.HasSuffix(meta, ";base64") {
			return nil, "", errors.New("media.base64: malformed data URL")
		}
		mime, s = strings.TrimSuffix(meta, ";base64"), payload
	}
	if int64(base64.StdEncoding.DecodedLen(len(s))) > max+2 {
		return nil, "", ErrTooLarge
	}
	data, err = base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, "", errors.New("media.base64 is not valid base64")
	}
	if int64(len(data)) > max {
		return nil, "", ErrTooLarge
	}
	return data, mime, nil
}

// Signer makes temporary URLs to received media that work without a token (spec 4.5).
type Signer struct {
	Key     []byte
	BaseURL string // e.g. http://127.0.0.1:8787 or the public_url
	TTL     time.Duration
	Now     func() time.Time
}

func NewSigner(key []byte, baseURL string) *Signer {
	return &Signer{Key: key, BaseURL: strings.TrimRight(baseURL, "/"), TTL: time.Hour, Now: time.Now}
}

// LoadOrCreateKey reads the signing secret at path (hex), creating a random one (0600)
// the first time. Deleting the file invalidates every signed URL.
func LoadOrCreateKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(key) < 32 {
			return nil, fmt.Errorf("media key %s is corrupt", path)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *Signer) mac(instanceID, mediaID string, exp int64) string {
	h := hmac.New(sha256.New, s.Key)
	fmt.Fprintf(h, "%s/%s/%d", instanceID, mediaID, exp)
	return hex.EncodeToString(h.Sum(nil))
}

// URL returns the signed URL of a media item and when it expires.
func (s *Signer) URL(instanceID, mediaID string) (string, time.Time) {
	exp := s.Now().Add(s.TTL).Truncate(time.Second)
	q := url.Values{"exp": {strconv.FormatInt(exp.Unix(), 10)}, "sig": {s.mac(instanceID, mediaID, exp.Unix())}}
	return fmt.Sprintf("%s/media/%s/%s?%s", s.BaseURL, url.PathEscape(instanceID), url.PathEscape(mediaID), q.Encode()), exp
}

// Verify checks a signed URL's exp and sig.
func (s *Signer) Verify(instanceID, mediaID, exp, sig string) bool {
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || s.Now().Unix() > e {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.mac(instanceID, mediaID, e)))
}
