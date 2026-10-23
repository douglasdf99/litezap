package mcp

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/secure"
	"github.com/douglasdf99/litezap/internal/service"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// StatusReplyBackend is the optional status reply part of a Backend (STA-4).
type StatusReplyBackend interface {
	ReplyStatus(ctx context.Context, id, statusID string, in service.StatusReplyInput, wait time.Duration) (service.MessageView, error)
}

// StatusReplyService is the subset of service.Social used by Local for status replies.
type StatusReplyService interface {
	ReplyStatus(ctx context.Context, instanceID, msgID string, in service.StatusReplyInput, wait time.Duration) (service.MessageView, error)
}

var (
	_ StatusReplyBackend = (*Local)(nil)
	_ StatusReplyBackend = (*REST)(nil)
)

func (l *Local) ReplyStatus(ctx context.Context, id, statusID string, in service.StatusReplyInput, wait time.Duration) (service.MessageView, error) {
	s, err := l.social(id)
	if err != nil {
		return service.MessageView{}, err
	}
	r, ok := s.(StatusReplyService)
	if !ok {
		return service.MessageView{}, apierr.New(http.StatusNotImplemented, "not_implemented", "not available on this server", false)
	}
	return r.ReplyStatus(ctx, id, statusID, in, wait)
}

func (r *REST) ReplyStatus(ctx context.Context, id, statusID string, in service.StatusReplyInput, wait time.Duration) (service.MessageView, error) {
	var v service.MessageView
	err := r.call(ctx, restTimeout+wait, "POST", instPath(id, "status", url.PathEscape(statusID), "reply"), waitQuery(wait), in, &v)
	return v, err
}

type replyStatusInput struct {
	InstanceID      string `json:"instance_id,omitempty" jsonschema:"Instance that received the status"`
	StatusID        string `json:"status_id" jsonschema:"Id of the contact's status, from list_status_updates."`
	Type            string `json:"type,omitempty" jsonschema:"text (default), gif or sticker."`
	Text            string `json:"text,omitempty" jsonschema:"The reply (type text)."`
	Caption         string `json:"caption,omitempty" jsonschema:"Caption of a gif reply (optional)."`
	MediaURL        string `json:"media_url,omitempty" jsonschema:"Public URL of the GIF (an MP4 video) or sticker (a WebP image)."`
	ClientMessageID string `json:"client_message_id,omitempty" jsonschema:"Your idempotency key (up to 128 chars)."`
	Wait            *bool  `json:"wait,omitempty" jsonschema:"Wait for the outcome before returning (default true)."`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty" jsonschema:"How long to wait, in seconds (default 30, max 55)."`
}

// registerStatusReply adds reply_status when the backend supports it.
func (t *tools) registerStatusReply(s *sdk.Server) {
	rb, ok := t.b.(StatusReplyBackend)
	if !ok {
		return
	}
	add(t, s, &sdk.Tool{
		Name:  "reply_status",
		Title: "Reply to a contact's status",
		Description: "Answers a contact's status (story) privately: a text, GIF or sticker sent to the status author in your direct chat, quoting the status, as replying from the app does. " +
			"It reaches a real person: only reply when the user asks, with the content they gave, to the status they picked. Same anti-ban queue as send_message (queued: do not repeat it).",
		InputSchema: schema[replyStatusInput](map[string]func(*jsonschema.Schema){
			"instance_id": func(p *jsonschema.Schema) { p.Description = instanceIDDoc },
			"wait":        boolDefault(true), "timeout_seconds": intRange(30, 1, maxWaitSeconds),
			"type": func(p *jsonschema.Schema) { p.Enum = []any{"text", "gif", "sticker"} },
		}),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, in replyStatusInput) (*sdk.CallToolResult, sendOutput, error) {
		id, err := t.instance(ctx, in.InstanceID)
		if err != nil {
			return nil, sendOutput{}, err
		}
		req := service.StatusReplyInput{Type: in.Type, Text: in.Text, Caption: in.Caption, ClientMessageID: in.ClientMessageID}
		if in.MediaURL != "" {
			req.Media = &service.MediaInput{URL: in.MediaURL}
		}
		if req.ClientMessageID == "" {
			req.ClientMessageID = secure.NewID("mcp_") // a retried call never replies twice
		}
		v, err := rb.ReplyStatus(ctx, id, in.StatusID, req, waitOf(in.Wait, in.TimeoutSeconds))
		return nil, queuedOutput(v), err
	})
}
