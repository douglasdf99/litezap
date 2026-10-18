package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/instance"
	"github.com/douglasdf99/litezap/internal/store"
)

// The MCP tools (send_message, send_media, send_reaction, send_location, send_contact,
// edit_message, delete_message) all reach Messaging.Send through the same wrapper.
func TestGuardedMessagingRefusesEverySendWhileSuspended(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateInstance(ctx, store.Instance{ID: "i1", Name: "i1", State: instance.Connected, TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	s := &Sending{Store: st}
	if err := s.SetInstance(ctx, "i1", true, "test"); err != nil {
		t.Fatal(err)
	}
	g := GuardedMessaging{Messaging: nil, Sending: s} // never reached while suspended
	for _, typ := range []string{"text", "reaction", "location", "contact", "edit", "revoke", "image"} {
		_, err := g.Send(ctx, "i1", SendInput{Type: typ}, 0)
		var ae *apierr.Error
		if !errors.As(err, &ae) || ae.Code != "sending_suspended" {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	if _, err := g.CheckNumbers(ctx, "i1", []string{"5511988887777"}); err == nil {
		t.Fatal("check-numbers allowed while suspended")
	}
	if err := g.SubscribePresence(ctx, "i1", "5511988887777"); err == nil {
		t.Fatal("presence subscription allowed while suspended")
	}
}
