package worker

import (
	"context"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

// STA-4: a GIF or sticker answering a status quotes it from status@broadcast.
func TestStatusReplyMediaQuotesStatus(t *testing.T) {
	w, dir := composeAdapter(t)
	ctx := context.Background()
	author := "5511999990000@s.whatsapp.net"
	q := &workerv1.Quote{MessageId: "ST1", Sender: author, Text: "praia", RemoteJid: "status@broadcast"}
	gif, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "gif", Mime: "video/mp4", Text: "kkk", MediaPath: stage(t, dir, "g", []byte("mp4")), Quote: q})
	if err != nil {
		t.Fatal(err)
	}
	st, err := w.build(ctx, dm, &workerv1.SendRequest{Type: "sticker", Mime: "image/webp", MediaPath: stage(t, dir, "s", []byte("RIFF")), Quote: q})
	if err != nil {
		t.Fatal(err)
	}
	for _, ci := range []*waE2E.ContextInfo{gif.GetVideoMessage().GetContextInfo(), st.GetStickerMessage().GetContextInfo()} {
		if ci.GetRemoteJID() != "status@broadcast" || ci.GetStanzaID() != "ST1" || ci.GetParticipant() != author ||
			ci.GetQuotedMessage().GetConversation() != "praia" {
			t.Fatalf("context = %v", ci)
		}
	}
	if !gif.GetVideoMessage().GetGifPlayback() || gif.GetVideoMessage().GetCaption() != "kkk" {
		t.Fatalf("gif = %v", gif)
	}
}
