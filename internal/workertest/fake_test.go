package workertest

import (
	"context"
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
