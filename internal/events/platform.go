package events

// TypeInstancePaired reports a completed pairing (marco 11): the instance went from
// waiting for a QR scan or phone code to connected. Published once per pairing, not on
// reconnections; via_link tells whether a hosted pairing link was live at the time (it
// is spent by this pairing).
const TypeInstancePaired = "instance.paired"

// Registered here (not in the Types literal) to keep this addition self-contained.
func init() { Types = append(Types, TypeInstancePaired) }

// InstancePaired is the data of instance.paired.
type InstancePaired struct {
	// Phone is the paired number (digits, no device suffix).
	Phone string `json:"phone,omitempty"`
	// ViaLink is true when a live pairing link was spent by this pairing.
	ViaLink bool `json:"via_link"`
}
