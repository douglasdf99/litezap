package mcp

import (
	"errors"
	"log/slog"

	"github.com/douglasdf99/litezap/internal/apierr"
)

// hints tell the model what to do about each API error code.
var hints = map[string]string{
	"unauthorized":           "The LiteZap token is missing or invalid; the user must fix the MCP configuration.",
	"forbidden":              "This token is not allowed to do that (an instance token only reaches its own instance).",
	"instance_not_found":     "Call list_instances to see the valid instance ids.",
	"instance_not_connected": "The WhatsApp number is not connected right now. Check get_instance_status; if it is waiting for pairing (qr_pending), use get_qr and ask the user to scan the code with their phone.",
	"instance_banned":        "WhatsApp banned this number; nothing can be sent from it. Tell the user.",
	"instance_not_pairing":   "Pairing is only possible while the instance state is qr_pending; check get_instance_status.",
	"not_found":              "Check the id or chat; list_chats and get_messages show what exists.",
	"conflict":               "This client_message_id was already used for a different message; use a new id.",
	"invalid_number":         "The recipient number is malformed or has no WhatsApp; verify it with check_numbers.",
	"rate_limited":           "LiteZap limits number lookups per instance to protect it from bans. Wait and try later; do not retry in a loop.",
	"validation_failed":      "Fix the arguments and call the tool again.",
	"media_fetch_failed":     "The media URL could not be downloaded; check that it is public and reachable.",
	"media_too_large":        "The file is over the size limit for this media type.",
	"message_expired":        "The message was not sent before it expired (it waited too long in the queue, e.g. outside the send window or while disconnected).",
	"worker_timeout":         "Temporary failure talking to WhatsApp; try again in a moment.",
	"worker_error":           "Temporary failure talking to WhatsApp; try again in a moment.",
	"shutting_down":          "The LiteZap server is restarting; try again in a moment.",
	"server_unreachable":     "Start `litezap serve` (or fix LITEZAP_URL) and try again.",
}

// sendHints replace hints for errors of send_message/send_media: there a timeout or a
// lost connection is ambiguous (the message may be queued already), and forbidden or
// invalid_number come from WhatsApp, not from the token.
var sendHints = map[string]string{
	"forbidden":          "WhatsApp refused this message (for example a group where only admins can write).",
	"worker_timeout":     ambiguousSend,
	"worker_error":       ambiguousSend,
	"server_unreachable": ambiguousSend,
	"shutting_down":      ambiguousSend,
	"internal":           ambiguousSend,
}

const ambiguousSend = "The message may already be queued or sent: check get_message_status (or get_messages for the chat) before sending again."

// sendFailure is an error of a send tool, with the ids the model needs to follow up.
type sendFailure struct {
	err       error
	clientID  string
	messageID string // empty when the message was not queued (or it is unknown)
}

func (f *sendFailure) Error() string { return f.err.Error() }
func (f *sendFailure) Unwrap() error { return f.err }

// toolError turns a backend error into the message of an MCP tool error (isError).
// Errors that are not API errors are logged and reported as internal, so store or
// network details never reach the model.
func toolError(log *slog.Logger, tool string, err error) error {
	var e *apierr.Error
	if !errors.As(err, &e) {
		log.Error("mcp tool failed", "tool", tool, "err", err)
		e = apierr.Internal()
	}
	msg := e.Code + ": " + e.Message
	var sf *sendFailure
	isSend := errors.As(err, &sf)
	h := hints[e.Code]
	if sh, ok := sendHints[e.Code]; isSend && ok {
		h = sh
	}
	if h != "" {
		msg += ". " + h
	}
	if isSend {
		if sf.messageID != "" {
			msg += " Message id: " + sf.messageID + "."
		}
		if e.Retryable {
			return errors.New(msg + " If you retry, pass client_message_id \"" + sf.clientID + "\" so it is never sent twice.")
		}
	}
	if e.Retryable {
		msg += " (retryable)"
	} else {
		msg += " (not retryable as is)"
	}
	return errors.New(msg)
}
