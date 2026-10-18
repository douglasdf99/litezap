package events

import "time"

// TypeInstanceProxyFailed reports an instance that cannot reach WhatsApp through its
// proxy (cloud spec E6): published when the instance enters reconnecting with reason
// proxy_unreachable, then again every few minutes while it lasts. The recovery is the
// next instance.status.
const TypeInstanceProxyFailed = "instance.proxy_failed"

// Registered here (not in the Types literal) to keep this addition self-contained.
func init() { Types = append(Types, TypeInstanceProxyFailed) }

// InstanceProxyFailed is the data of instance.proxy_failed.
type InstanceProxyFailed struct {
	Reason string `json:"reason"` // proxy_unreachable
	// Since is when the current outage began; DurationSeconds how long it has lasted.
	Since           time.Time `json:"since"`
	DurationSeconds int64     `json:"duration_seconds"`
	// OutagesLastHour counts the outages that began in the last hour, this one included.
	OutagesLastHour int `json:"outages_last_hour"`
	// Repeat is 0 for the first notice of an outage and grows with each reminder.
	Repeat int `json:"repeat"`
	// Error is the last connect error reported by the worker (credentials, phone numbers
	// and tokens removed; empty when the worker gave none).
	Error string `json:"error,omitempty"`
}
