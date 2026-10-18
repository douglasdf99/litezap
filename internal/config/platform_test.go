package config

import (
	"slices"
	"testing"
)

func TestPlatformEnvAndValidation(t *testing.T) {
	t.Setenv("LITEZAP_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.1")
	t.Setenv("LITEZAP_ADMIN_ALLOWED_IPS", "203.0.113.0/24")
	t.Setenv("LITEZAP_PAIRING_FRAME_ANCESTORS", "https://App.example.com/, https://*.partner.io:8443,http://localhost:3000")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.PairingFrameAncestors, []string{"https://app.example.com", "https://*.partner.io:8443", "http://localhost:3000"}) {
		t.Errorf("ancestors = %v", cfg.PairingFrameAncestors)
	}
	trusted, adminIPs, op := cfg.IPLists()
	if len(trusted) != 2 || len(adminIPs) != 1 || len(op) != 0 {
		t.Errorf("lists = %v %v %v", trusted, adminIPs, op)
	}
	for name, env := range map[string]map[string]string{
		"bad cidr":            {"LITEZAP_TRUSTED_PROXIES": "10.0.0.0/40"},
		"bad admin ip":        {"LITEZAP_ADMIN_ALLOWED_IPS": "example.com"},
		"bad operator ip":     {"LITEZAP_OPERATOR_ALLOWED_IPS": "1.2.3"},
		"ancestor wildcard":   {"LITEZAP_PAIRING_FRAME_ANCESTORS": "*"},
		"ancestor csp inject": {"LITEZAP_PAIRING_FRAME_ANCESTORS": "https://a.com; script-src *"},
		"ancestor http":       {"LITEZAP_PAIRING_FRAME_ANCESTORS": "http://a.com"},
		"ancestor path":       {"LITEZAP_PAIRING_FRAME_ANCESTORS": "https://a.com/x"},
		"ancestor quote":      {"LITEZAP_PAIRING_FRAME_ANCESTORS": "'self'"},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(""); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
