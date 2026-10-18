package mcp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A send that WhatsApp refuses fails with a WhatsApp-side hint and the message id.
func TestFailedSendKeepsIDAndHint(t *testing.T) {
	for _, m := range []mode{modeLocal, modeREST} {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Refused")
			e.sp.Last(id).WA.SetSendFunc(func(*workerv1.SendRequest) (*workerv1.SendResponse, error) {
				return nil, status.Error(codes.PermissionDenied, "forbidden: no permission to send in this group")
			})
			cs := e.client(m, tok)
			res := call(t, cs, "send_message", map[string]any{"to": "120363000000000000@g.us", "text": "oi"})
			msg := text(res)
			if !res.IsError || !strings.Contains(msg, "WhatsApp refused") || !strings.Contains(msg, "Message id: msg_") {
				t.Fatalf("failed send = %v %q", res.IsError, msg)
			}
			if strings.Contains(msg, "instance token") {
				t.Fatalf("token hint on a WhatsApp refusal: %q", msg)
			}
		})
	}
}

// Ambiguous send failures ask to check before resending and carry the idempotency key.
func TestAmbiguousSendErrorCarriesClientID(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	err := toolError(log, "send_message", &sendFailure{err: apierr.New(504, "worker_timeout", "slow", true), clientID: "mcp_abc"})
	for _, frag := range []string{"get_message_status", `client_message_id "mcp_abc"`, "may already be queued"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("%q lacks %q", err, frag)
		}
	}
	if strings.Contains(err.Error(), "try again in a moment") {
		t.Errorf("ambiguous send error invites a blind retry: %q", err)
	}
	// Outside sends the generic hint stays.
	if err := toolError(log, "list_chats", apierr.New(504, "worker_timeout", "slow", true)); !strings.Contains(err.Error(), "try again") {
		t.Errorf("generic hint missing: %q", err)
	}
	// Non-API errors never leak details.
	if err := toolError(log, "list_chats", errors.New("sqlite: disk I/O error at /secret/path")); strings.Contains(err.Error(), "secret") {
		t.Errorf("internal error leaked: %q", err)
	}
}

// The tool generates a client_message_id, so the wait never queues a second copy.
func TestSendGeneratesClientID(t *testing.T) {
	e := newEnv(t, nil)
	id, tok := e.connected("Key")
	var out sendOutput
	ok(t, e.client(modeREST, tok), "send_message", map[string]any{"to": "5511988887777", "text": "uma vez"}, &out)
	if !strings.HasPrefix(out.Message.ClientMessageID, "mcp_") || !out.Sent {
		t.Fatalf("send = %+v", out)
	}
	if n := len(e.sp.Last(id).WA.Sent()); n != 1 {
		t.Fatalf("worker sent %d messages", n)
	}
}

// Every POST of a stateless HTTP session is scoped by its own token.
func TestHTTPScopeFollowsEachRequestToken(t *testing.T) {
	e := newEnv(t, nil)
	idA, tokA := e.create("A")
	e.create("B")
	var cur atomic.Value
	cur.Store(admin)
	c := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "v"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: e.srv.URL + "/mcp",
		DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: switching{&cur}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	var list listInstancesOutput
	ok(t, cs, "list_instances", nil, &list)
	if len(list.Instances) != 2 {
		t.Fatalf("admin list = %+v", list)
	}
	cur.Store(tokA)
	ok(t, cs, "list_instances", nil, &list)
	if len(list.Instances) != 1 || list.Instances[0].ID != idA {
		t.Fatalf("after switching to the instance token = %+v", list)
	}
}

type switching struct{ tok *atomic.Value }

func (s switching) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+s.tok.Load().(string))
	return http.DefaultTransport.RoundTrip(r)
}

// Hostile ids stay one escaped path segment of the caller's own instance.
func TestRESTHostileIDs(t *testing.T) {
	e := newEnv(t, nil)
	_, tokA := e.connected("A")
	idB, _ := e.connected("B")
	cs := e.client(modeREST, tokA)
	for _, msgID := range []string{"../../" + idB, "a/b", "x?y=1", "%2e%2e"} {
		fails(t, cs, "get_message_status", map[string]any{"message_id": msgID}, "not_found")
	}
	// A slash in a JID user part is one escaped segment: an empty chat of the own instance.
	var page service.MessagePage
	ok(t, cs, "get_messages", map[string]any{"chat": "../" + idB + "/chats/x@s.whatsapp.net"}, &page)
	if len(page.Messages) != 0 {
		t.Fatalf("hostile chat = %+v", page)
	}
}

// Messages with media and received messages fit the output schemas.
func TestOutputSchemasWithMedia(t *testing.T) {
	for _, m := range []mode{modeLocal, modeHTTP} {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Pic")
			e.sp.Last(id).EmitMessage(&workerv1.Message{Id: "IMG1", Chat: "5511988887777@s.whatsapp.net",
				Sender: "5511988887777@s.whatsapp.net", TimestampMs: 1_800_000_000_000, Type: "image", Text: "foto",
				Media: &workerv1.Media{Kind: "image", Mime: "image/jpeg", Size: 1234, Ref: []byte("ref")}})
			cs := e.client(m, tok)
			var page service.MessagePage
			eventually(t, "the image message", func() bool {
				ok(t, cs, "get_messages", map[string]any{"chat": "5511988887777"}, &page)
				return len(page.Messages) == 1
			})
			if page.Messages[0].Media == nil || page.Messages[0].Media.URL == "" {
				t.Fatalf("media = %+v", page.Messages[0])
			}
			var v service.MessageView
			ok(t, cs, "get_message_status", map[string]any{"message_id": page.Messages[0].ID}, &v)
			if v.Type != "image" {
				t.Fatalf("received message status = %+v", v)
			}
		})
	}
}

// The scope matrix also covers the remaining tools.
func TestInstanceScopeOtherTools(t *testing.T) {
	e := newEnv(t, nil)
	_, tokA := e.create("A")
	idB, _ := e.create("B")
	ci := e.client(modeLocal, tokA)
	for tool, args := range map[string]map[string]any{
		"get_message_status": {"message_id": "msg_x"},
		"get_contact":        {"contact": "5511988887777"},
		"send_media":         {"to": "5511988887777", "type": "image", "url": "https://example.com/a.png"},
	} {
		args["instance_id"] = idB
		fails(t, ci, tool, args, "forbidden")
	}
}
