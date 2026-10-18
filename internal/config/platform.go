package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/douglasdf99/litezap/internal/ipallow"
)

// Platform options (marco 11). Kept in their own struct, embedded inline in Config, so
// the YAML keys stay flat.
type Platform struct {
	// TrustedProxies are the reverse proxies (IPs or CIDRs) whose X-Forwarded-For is
	// believed when resolving the client address for IP allowlists. Empty: the header is
	// ignored and the TCP peer is the client.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// AdminAllowedIPs and OperatorAllowedIPs restrict where the admin and operator tokens
	// are accepted from (empty: anywhere).
	AdminAllowedIPs    []string `yaml:"admin_allowed_ips"`
	OperatorAllowedIPs []string `yaml:"operator_allowed_ips"`
	// PairingFrameAncestors are the origins allowed to embed the hosted pairing page in an
	// iframe (CSP frame-ancestors). Empty: the page cannot be framed.
	PairingFrameAncestors []string `yaml:"pairing_frame_ancestors"`
}

func envList(name string, dst *[]string) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	*dst = nil
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
}

func applyPlatformEnv(cfg *Config) {
	envList("LITEZAP_TRUSTED_PROXIES", &cfg.TrustedProxies)
	envList("LITEZAP_ADMIN_ALLOWED_IPS", &cfg.AdminAllowedIPs)
	envList("LITEZAP_OPERATOR_ALLOWED_IPS", &cfg.OperatorAllowedIPs)
	envList("LITEZAP_PAIRING_FRAME_ANCESTORS", &cfg.PairingFrameAncestors)
}

// frameAncestorRe accepts an HTTPS origin (optionally *.domain) or a loopback HTTP origin.
// Nothing else reaches the CSP header: no paths, spaces, quotes or semicolons.
var frameAncestorRe = regexp.MustCompile(`^(https://(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*|http://(localhost|127\.0\.0\.1|\[::1\]))(:[0-9]{1,5})?$`)

func validatePlatform(cfg *Config) error {
	for name, v := range map[string][]string{"trusted_proxies": cfg.TrustedProxies,
		"admin_allowed_ips": cfg.AdminAllowedIPs, "operator_allowed_ips": cfg.OperatorAllowedIPs} {
		if _, err := ipallow.Parse(v); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	out := make([]string, 0, len(cfg.PairingFrameAncestors))
	for _, raw := range cfg.PairingFrameAncestors {
		o := strings.ToLower(strings.TrimRight(strings.TrimSpace(raw), "/"))
		if !frameAncestorRe.MatchString(o) {
			return fmt.Errorf("pairing_frame_ancestors: %q must be an origin like https://app.example.com (or https://*.example.com)", raw)
		}
		out = append(out, o)
	}
	cfg.PairingFrameAncestors = out
	return nil
}

// IPLists parses the (already validated) platform lists.
func (c Config) IPLists() (trusted, admin, operator ipallow.List) {
	trusted, _ = ipallow.Parse(c.TrustedProxies)
	admin, _ = ipallow.Parse(c.AdminAllowedIPs)
	operator, _ = ipallow.Parse(c.OperatorAllowedIPs)
	return
}
