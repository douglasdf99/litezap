// Package instance defines the lifecycle states of a WhatsApp instance.
package instance

type State string

const (
	Created      State = "created"
	Starting     State = "starting"
	QRPending    State = "qr_pending"
	Connecting   State = "connecting"
	Connected    State = "connected"
	Reconnecting State = "reconnecting"
	LoggedOut    State = "logged_out"
	Banned       State = "banned"
	Failed       State = "failed"
	Stopped      State = "stopped"
)

// Active reports whether a worker is expected to be running in this state.
func (s State) Active() bool {
	switch s {
	case Starting, QRPending, Connecting, Connected, Reconnecting:
		return true
	}
	return false
}

// Terminal reports whether the state needs human action before restarting.
func (s State) Terminal() bool {
	return s == LoggedOut || s == Banned || s == Failed
}

var edges = map[State][]State{
	Created: {Starting, Stopped},
	// The worker's event snapshot carries only its latest state, so from starting
	// any worker-reported state may arrive first.
	Starting:     {QRPending, Connecting, Connected, Reconnecting},
	QRPending:    {Connecting, Connected},
	Connecting:   {Connected, Reconnecting},
	Connected:    {Reconnecting},
	Reconnecting: {Connected},
	LoggedOut:    {Starting, Stopped},
	Failed:       {Starting, Stopped},
	Stopped:      {Starting},
}

// CanTransition implements the transition table of spec section 9.1.
func CanTransition(from, to State) bool {
	if from == to {
		return true
	}
	if from == Banned {
		return false
	}
	if from.Active() {
		switch to {
		case LoggedOut, Banned, Failed, Starting, Stopped:
			return true
		}
	}
	for _, s := range edges[from] {
		if s == to {
			return true
		}
	}
	return false
}
