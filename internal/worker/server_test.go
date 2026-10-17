package worker

import (
	"context"
	"net"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type stubWA struct {
	sink    Sink
	logouts int
}

func (s *stubWA) Run(ctx context.Context, sink Sink) error {
	s.sink = sink
	sink.ConnState("qr_pending", "", "")
	sink.QR("QR-1", time.Minute)
	return nil
}
func (s *stubWA) PairPhone(ctx context.Context, phone string) (string, error) {
	return "ABCD-EFGH", nil
}
func (s *stubWA) Logout(ctx context.Context) error {
	s.logouts++
	s.sink.ConnState("logged_out", "api", "")
	return nil
}
func (s *stubWA) Disconnect() {}

func dial(t *testing.T, srv *Server) workerv1.WorkerClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	workerv1.RegisterWorkerServer(g, srv)
	go g.Serve(lis)
	t.Cleanup(g.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return workerv1.NewWorkerClient(cc)
}

func TestServerSnapshotAndLiveEvents(t *testing.T) {
	wa := &stubWA{}
	srv := NewServer(wa)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := stream.Recv()
	if err != nil || ev.GetConn().GetState() != "qr_pending" {
		t.Fatalf("first event = %v, %v", ev, err)
	}
	ev, err = stream.Recv()
	if err != nil || ev.GetQr().GetCode() != "QR-1" {
		t.Fatalf("second event = %v, %v", ev, err)
	}

	ping, err := cli.Ping(ctx, &workerv1.PingRequest{})
	if err != nil || ping.State != "qr_pending" {
		t.Fatalf("ping = %v, %v", ping, err)
	}

	pair, err := cli.PairPhone(ctx, &workerv1.PairPhoneRequest{Phone: "5511999999999"})
	if err != nil || pair.Code != "ABCD-EFGH" {
		t.Fatalf("pair = %v, %v", pair, err)
	}

	if _, err := cli.Logout(ctx, &workerv1.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	ev, err = stream.Recv()
	if err != nil || ev.GetConn().GetState() != "logged_out" {
		t.Fatalf("after logout = %v, %v", ev, err)
	}
	if wa.logouts != 1 {
		t.Fatalf("logouts = %d", wa.logouts)
	}
}

func TestServerDropsQRSnapshotAfterConnect(t *testing.T) {
	wa := &stubWA{}
	srv := NewServer(wa)
	srv.Start(context.Background())
	srv.ConnState("connected", "", "5511@s.whatsapp.net")

	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ev, _ := stream.Recv()
	if ev.GetConn().GetState() != "connected" {
		t.Fatalf("snapshot = %v", ev)
	}
	srv.QR("late", time.Minute) // QR after connect must not be replayed later, but is broadcast live
	ev, _ = stream.Recv()
	if ev.GetQr().GetCode() != "late" {
		t.Fatalf("live = %v", ev)
	}

	// A new subscriber's snapshot has the conn state only: the late QR is not replayed.
	stream2, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ev, err = stream2.Recv()
	if err != nil || ev.GetConn().GetState() != "connected" {
		t.Fatalf("second snapshot = %v, %v", ev, err)
	}
	got := make(chan *workerv1.Event, 1)
	go func() {
		if e, err := stream2.Recv(); err == nil {
			got <- e
		}
	}()
	select {
	case e := <-got:
		t.Fatalf("unexpected extra event in snapshot: %v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServerStopEndsEventStreams(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background())
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil { // snapshot
		t.Fatal(err)
	}
	srv.Stop()
	start := time.Now()
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	if time.Since(start) > time.Second {
		t.Fatalf("stream took %v to end", time.Since(start))
	}
}

func TestServerEmptyQRClearsSnapshot(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background()) // qr_pending + QR-1
	srv.QR("", 0)                   // the pairing round ended: the code is expired

	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := stream.Recv(); err != nil || ev.GetConn().GetState() != "qr_pending" {
		t.Fatalf("snapshot = %v, %v", ev, err)
	}
	got := make(chan *workerv1.Event, 1)
	go func() {
		if e, err := stream.Recv(); err == nil {
			got <- e
		}
	}()
	select {
	case e := <-got:
		t.Fatalf("expired QR replayed in snapshot: %v", e)
	case <-time.After(100 * time.Millisecond):
	}
}
