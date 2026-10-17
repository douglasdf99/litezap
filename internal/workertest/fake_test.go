package workertest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

func TestFakeEmitsAndCrashes(t *testing.T) {
	sp := NewSpawner()
	sp.OnStart = func(f *Fake) { f.Emit("qr_pending"); f.EmitQR("QR-9") }
	h, err := sp.Start(context.Background(), "inst_x")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := h.Client().Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ev, _ := stream.Recv(); ev.GetConn().GetState() != "qr_pending" {
		t.Fatalf("got %v", ev)
	}
	if ev, _ := stream.Recv(); ev.GetQr().GetCode() != "QR-9" {
		t.Fatalf("got %v", ev)
	}
	if sp.Starts("inst_x") != 1 || sp.Last("inst_x") == nil {
		t.Fatal("start not recorded")
	}
	sp.Last("inst_x").Crash()
	select {
	case <-h.Done():
	case <-time.After(time.Second):
		t.Fatal("done not closed")
	}
}

func TestFakeMessaging(t *testing.T) {
	sp := NewSpawner()
	h, err := sp.Start(context.Background(), "inst_m")
	if err != nil {
		t.Fatal(err)
	}
	f := sp.Last("inst_m")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := h.Client().Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.EmitMessage(&workerv1.Message{Id: "W1", Chat: "a@s.whatsapp.net", Type: "text", Text: "oi"})
	if ev, err := stream.Recv(); err != nil || ev.GetMessage().GetId() != "W1" {
		t.Fatalf("got %v, %v", ev, err)
	}
	resp, err := h.Client().Send(ctx, &workerv1.SendRequest{To: "a@s.whatsapp.net", MessageId: "M1", Type: "text"})
	if err != nil || resp.MessageId != "M1" || len(f.WA.Sent()) != 1 {
		t.Fatalf("send = %v, %v", resp, err)
	}
	f.WA.SetMedia("ref", []byte("bytes"))
	dest := filepath.Join(t.TempDir(), "m")
	dl, err := h.Client().DownloadMedia(ctx, &workerv1.DownloadMediaRequest{Kind: "image", Ref: []byte("ref"), DestPath: dest})
	if b, _ := os.ReadFile(dest); err != nil || dl.Size != 5 || string(b) != "bytes" {
		t.Fatalf("download = %v, %v (%q)", dl, err, b)
	}
	f.WA.SetUnregistered("5511000000000")
	res, err := h.Client().CheckNumbers(ctx, &workerv1.CheckNumbersRequest{Phones: []string{"5511999999999", "5511000000000"}})
	if err != nil || !res.Results[0].Exists || res.Results[0].Jid != "5511999999999@s.whatsapp.net" || res.Results[1].Exists {
		t.Fatalf("check = %v, %v", res, err)
	}
}
