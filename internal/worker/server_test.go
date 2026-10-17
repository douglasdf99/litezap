package worker

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
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

func msgEvent(id string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Message{Message: &workerv1.Message{Id: id, Chat: "c@s.whatsapp.net"}}}
}

func recvIDs(t *testing.T, stream workerv1.Worker_EventsClient, n int) []string {
	t.Helper()
	var ids []string
	for len(ids) < n {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv after %v: %v", ids, err)
		}
		if m := ev.GetMessage(); m != nil {
			ids = append(ids, m.Id)
		}
	}
	return ids
}

// Data events emitted while nobody is subscribed (offline messages right after connect)
// are delivered to the first subscriber, after the connection snapshot.
func TestServerKeepsDataEventsUntilSubscribed(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background())
	srv.ConnState("connected", "", "5511@s.whatsapp.net")
	for i := 0; i < 300; i++ { // more than any channel buffer
		srv.Data(msgEvent(fmt.Sprint(i)))
	}
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := stream.Recv(); err != nil || ev.GetConn().GetState() != "connected" {
		t.Fatalf("snapshot = %v, %v", ev, err)
	}
	ids := recvIDs(t, stream, 300)
	if ids[0] != "0" || ids[299] != "299" {
		t.Fatalf("order lost: first=%s last=%s", ids[0], ids[299])
	}
	srv.Data(msgEvent("live"))
	if got := recvIDs(t, stream, 1); got[0] != "live" {
		t.Fatalf("live = %v", got)
	}
}

func TestServerReturnsUnsentDataToBacklog(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background())
	cli := dial(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stream.Recv() // qr_pending snapshot
	cancel()      // the supervisor goes away...
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.subs)
		srv.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscriber not removed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv.Data(msgEvent("while-away")) // ...and comes back later
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	stream2, err := cli.Events(ctx2, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := recvIDs(t, stream2, 1); got[0] != "while-away" {
		t.Fatalf("got %v", got)
	}
}

func histEvent(chat string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_History{History: &workerv1.HistoryChunk{Chat: chat}}}
}

// Live messages and states go before history chunks queued earlier.
func TestServerSendsLiveBeforeHistory(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background()) // qr_pending + QR-1
	srv.Data(histEvent("h1"))
	srv.Data(histEvent("h2"))
	srv.Data(msgEvent("m1"))
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for len(order) < 5 {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case ev.GetConn() != nil:
			order = append(order, "conn")
		case ev.GetQr() != nil:
			order = append(order, "qr")
		case ev.GetMessage() != nil:
			order = append(order, ev.GetMessage().Id)
		case ev.GetHistory() != nil:
			order = append(order, ev.GetHistory().Chat)
		}
	}
	want := []string{"conn", "qr", "m1", "h1", "h2"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// With historySlots chunks waiting, the producer (whatsmeow's history handler) blocks
// instead of dropping, until a subscriber takes one.
func TestServerHistoryBackpressure(t *testing.T) {
	srv := NewServer(&stubWA{})
	srv.Start(context.Background())
	for i := 0; i < historySlots; i++ {
		srv.Data(histEvent(fmt.Sprint(i)))
	}
	returned := make(chan struct{})
	go func() { srv.Data(histEvent("extra")); close(returned) }()
	select {
	case <-returned:
		t.Fatal("history producer did not block on a full queue")
	case <-time.After(50 * time.Millisecond):
	}
	cli := dial(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := cli.Events(ctx, &workerv1.EventsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for got < historySlots+1 {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if ev.GetHistory() != nil {
			got++
		}
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("history producer still blocked")
	}
	if srv.Dropped() != 0 {
		t.Fatalf("dropped = %d", srv.Dropped())
	}
}

func TestPushLiveNeverDropsConnState(t *testing.T) {
	srv := NewServer(&stubWA{})
	q := []*workerv1.Event{{Kind: &workerv1.Event_Conn{Conn: &workerv1.ConnState{State: "connected"}}}}
	for i := 0; i < maxLive+5; i++ {
		q = srv.pushLive(q, msgEvent(fmt.Sprint(i)))
	}
	if len(q) != maxLive || q[0].GetConn() == nil || q[1].GetMessage().Id != "6" {
		t.Fatalf("len=%d first=%v second=%v", len(q), q[0], q[1])
	}
	if srv.Dropped() != 6 {
		t.Fatalf("dropped = %d, want 6", srv.Dropped())
	}
	// Returning unsent events to a full backlog keeps the cap too.
	back := srv.capLive(append(q, q...))
	if len(back) != maxLive || back[0].GetConn() == nil {
		t.Fatalf("capped backlog len=%d first=%v", len(back), back[0])
	}
}

type stubMessenger struct {
	stubWA
	sent []*workerv1.SendRequest
}

func (m *stubMessenger) Send(_ context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
	m.sent = append(m.sent, req)
	return &workerv1.SendResponse{MessageId: req.MessageId, TimestampMs: 1}, nil
}
func (m *stubMessenger) SendPresence(context.Context, *workerv1.SendPresenceRequest) error {
	return nil
}
func (m *stubMessenger) MarkRead(context.Context, *workerv1.MarkReadRequest) error { return nil }
func (m *stubMessenger) DownloadMedia(_ context.Context, kind string, ref []byte, dest string) (int64, error) {
	return int64(len(kind) + len(ref) + len(dest)), nil
}
func (m *stubMessenger) Contacts(context.Context) ([]*workerv1.Contact, error) {
	return []*workerv1.Contact{{Jid: "a@s.whatsapp.net", Name: "A"}}, nil
}
func (m *stubMessenger) Contact(_ context.Context, jid string) (*workerv1.Contact, bool, error) {
	return &workerv1.Contact{Jid: jid}, jid == "a@s.whatsapp.net", nil
}
func (m *stubMessenger) CheckNumbers(_ context.Context, phones []string) ([]*workerv1.NumberStatus, error) {
	return []*workerv1.NumberStatus{{Phone: phones[0], Exists: true}}, nil
}

func TestServerMessagingRPCs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	plain := dial(t, NewServer(&stubWA{}))
	if _, err := plain.Send(ctx, &workerv1.SendRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("send without messenger = %v", err)
	}

	m := &stubMessenger{}
	cli := dial(t, NewServer(m))
	resp, err := cli.Send(ctx, &workerv1.SendRequest{To: "a@s.whatsapp.net", MessageId: "ID1", Type: "text", Text: "oi"})
	if err != nil || resp.MessageId != "ID1" || len(m.sent) != 1 {
		t.Fatalf("send = %v, %v", resp, err)
	}
	dl, err := cli.DownloadMedia(ctx, &workerv1.DownloadMediaRequest{Kind: "image", Ref: []byte("r"), DestPath: "/d"})
	if err != nil || dl.Size != 8 {
		t.Fatalf("download = %v, %v", dl, err)
	}
	cs, err := cli.GetContacts(ctx, &workerv1.GetContactsRequest{})
	if err != nil || len(cs.Contacts) != 1 {
		t.Fatalf("contacts = %v, %v", cs, err)
	}
	c, err := cli.GetContact(ctx, &workerv1.GetContactRequest{Jid: "b@s.whatsapp.net"})
	if err != nil || c.Found {
		t.Fatalf("contact = %v, %v", c, err)
	}
	nums, err := cli.CheckNumbers(ctx, &workerv1.CheckNumbersRequest{Phones: []string{"5511"}})
	if err != nil || !nums.Results[0].Exists {
		t.Fatalf("check = %v, %v", nums, err)
	}
	if _, err := cli.MarkRead(ctx, &workerv1.MarkReadRequest{Chat: "a@s.whatsapp.net", MessageIds: []string{"X"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.SendPresence(ctx, &workerv1.SendPresenceRequest{To: "a@s.whatsapp.net", State: "composing"}); err != nil {
		t.Fatal(err)
	}
}
