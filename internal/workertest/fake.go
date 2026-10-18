// Package workertest provides an in-memory worker and spawner for supervisor tests.
package workertest

import (
	"context"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/wire"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const testJID = "5511999999999:7@s.whatsapp.net"

// The fake must satisfy the messaging interface, or Server would answer Unimplemented.
var _ worker.Messenger = (*FakeWA)(nil)
var _ worker.Realtime = (*FakeWA)(nil)

// FakeWA is a scriptable worker.WAClient and worker.Messenger.
type FakeWA struct {
	mu       sync.Mutex
	sink     worker.Sink
	PairCode string
	Logouts  atomic.Int32
	// SilentLogout makes Logout succeed without reporting logged_out (a missed event).
	SilentLogout atomic.Bool

	msgMu        sync.Mutex
	sent         []*workerv1.SendRequest
	sentMedia    map[string][]byte // message id -> attachment read at Send time
	presences    []*workerv1.SendPresenceRequest
	reads        []*workerv1.MarkReadRequest
	sendFn       func(*workerv1.SendRequest) (*workerv1.SendResponse, error)
	media        map[string][]byte
	contacts     []*workerv1.Contact
	registered   map[string]string // phone -> JID, overriding the default
	unregistered map[string]bool
	checks       int   // phones looked up by CheckNumbers
	checkErr     error // returned by CheckNumbers when set
	downloads    int
	// Calls and presence (marco 7).
	offers     map[string]bool // call ids offered and not finished
	rejected   []string
	presence   []bool // SetPresence calls (true: available)
	subscribed []string
	rtErr      error // returned by the Realtime methods when set
}

func (f *FakeWA) Run(ctx context.Context, sink worker.Sink) error {
	f.mu.Lock()
	f.sink = sink
	f.mu.Unlock()
	return nil
}

func (f *FakeWA) getSink() worker.Sink {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sink
}

func (f *FakeWA) PairPhone(context.Context, string) (string, error) { return f.PairCode, nil }

func (f *FakeWA) Logout(context.Context) error {
	f.Logouts.Add(1)
	if f.SilentLogout.Load() {
		return nil
	}
	f.getSink().ConnState("logged_out", "api", "")
	return nil
}

func (f *FakeWA) Disconnect() {}

// SetSendFunc scripts Send; fn sees every request (nil restores the default success).
func (f *FakeWA) SetSendFunc(fn func(*workerv1.SendRequest) (*workerv1.SendResponse, error)) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.sendFn = fn
}

func (f *FakeWA) Send(_ context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
	var data []byte
	if req.MediaPath != "" {
		data, _ = os.ReadFile(req.MediaPath) // the queue deletes the file once the item is final
	}
	f.msgMu.Lock()
	f.sent = append(f.sent, req)
	if data != nil {
		if f.sentMedia == nil {
			f.sentMedia = map[string][]byte{}
		}
		f.sentMedia[req.MessageId] = data
	}
	fn := f.sendFn
	f.msgMu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return &workerv1.SendResponse{MessageId: req.MessageId, TimestampMs: time.Now().UnixMilli()}, nil
}

// Sent returns every Send request received so far (including failed ones).
func (f *FakeWA) Sent() []*workerv1.SendRequest {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.SendRequest(nil), f.sent...)
}

// SentMedia returns the attachment of a sent message, as read from its media_path.
func (f *FakeWA) SentMedia(messageID string) []byte {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.sentMedia[messageID]
}

func (f *FakeWA) SendPresence(_ context.Context, req *workerv1.SendPresenceRequest) error {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.presences = append(f.presences, req)
	return nil
}

func (f *FakeWA) Presences() []*workerv1.SendPresenceRequest {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.SendPresenceRequest(nil), f.presences...)
}

func (f *FakeWA) MarkRead(_ context.Context, req *workerv1.MarkReadRequest) error {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.reads = append(f.reads, req)
	return nil
}

func (f *FakeWA) Reads() []*workerv1.MarkReadRequest {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.MarkReadRequest(nil), f.reads...)
}

// SetMedia makes DownloadMedia write data for ref.
func (f *FakeWA) SetMedia(ref string, data []byte) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.media == nil {
		f.media = map[string][]byte{}
	}
	f.media[ref] = data
}

// Downloads counts DownloadMedia calls.
func (f *FakeWA) Downloads() int {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.downloads
}

func (f *FakeWA) DownloadMedia(_ context.Context, _ string, ref []byte, dest string) (int64, error) {
	time.Sleep(20 * time.Millisecond) // a download takes a while: concurrent callers overlap
	f.msgMu.Lock()
	f.downloads++
	data, ok := f.media[string(ref)]
	f.msgMu.Unlock()
	if !ok {
		return 0, status.Error(codes.NotFound, "media_unavailable: gone")
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return 0, status.Error(codes.Unknown, "worker_error: "+err.Error())
	}
	return int64(len(data)), nil
}

func (f *FakeWA) SetContacts(cs ...*workerv1.Contact) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.contacts = cs
}

func (f *FakeWA) Contacts(context.Context) ([]*workerv1.Contact, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]*workerv1.Contact(nil), f.contacts...), nil
}

func (f *FakeWA) Contact(_ context.Context, jid string) (*workerv1.Contact, bool, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	for _, c := range f.contacts {
		if c.Jid == jid {
			return c, true, nil
		}
	}
	return &workerv1.Contact{Jid: jid}, false, nil
}

// SetRegistered maps phone to a different JID (e.g. with the Brazilian 9th digit). By
// default every number exists as <phone>@s.whatsapp.net.
func (f *FakeWA) SetRegistered(phone, jid string) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.registered == nil {
		f.registered = map[string]string{}
	}
	f.registered[phone] = jid
}

// SetUnregistered makes phone a number without WhatsApp.
func (f *FakeWA) SetUnregistered(phone string) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.unregistered == nil {
		f.unregistered = map[string]bool{}
	}
	f.unregistered[phone] = true
}

// SetCheckErr makes CheckNumbers fail with err (nil restores normal answers).
func (f *FakeWA) SetCheckErr(err error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.checkErr = err
}

// Checks counts the phones looked up so far.
func (f *FakeWA) Checks() int {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return f.checks
}

func (f *FakeWA) CheckNumbers(_ context.Context, phones []string) ([]*workerv1.NumberStatus, error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.checks += len(phones)
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	out := make([]*workerv1.NumberStatus, 0, len(phones))
	for _, p := range phones {
		ns := &workerv1.NumberStatus{Phone: p}
		if !f.unregistered[p] {
			ns.Exists, ns.Jid = true, p+"@s.whatsapp.net"
			if jid, ok := f.registered[p]; ok {
				ns.Jid = jid
			}
		}
		out = append(out, ns)
	}
	return out, nil
}

// SetRealtimeErr makes RejectCall, SetPresence and SubscribePresence fail with err.
func (f *FakeWA) SetRealtimeErr(err error) {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	f.rtErr = err
}

func (f *FakeWA) RejectCall(_ context.Context, callID string) error {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.rtErr != nil {
		return f.rtErr
	}
	if !f.offers[callID] {
		return status.Error(codes.NotFound, "not_found: unknown or finished call")
	}
	delete(f.offers, callID)
	f.rejected = append(f.rejected, callID)
	return nil
}

// Rejected returns the call ids rejected so far.
func (f *FakeWA) Rejected() []string {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]string(nil), f.rejected...)
}

func (f *FakeWA) SetPresence(_ context.Context, available bool) error {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.rtErr != nil {
		return f.rtErr
	}
	f.presence = append(f.presence, available)
	return nil
}

// PresenceStates returns every SetPresence call (true: available).
func (f *FakeWA) PresenceStates() []bool {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]bool(nil), f.presence...)
}

func (f *FakeWA) SubscribePresence(_ context.Context, jid string) error {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	if f.rtErr != nil {
		return f.rtErr
	}
	f.subscribed = append(f.subscribed, jid)
	return nil
}

// Subscriptions returns the JIDs passed to SubscribePresence.
func (f *FakeWA) Subscriptions() []string {
	f.msgMu.Lock()
	defer f.msgMu.Unlock()
	return append([]string(nil), f.subscribed...)
}

// Fake is one in-memory worker; it implements spawner.WorkerHandle.
type Fake struct {
	ID        string
	WA        *FakeWA
	PingBlock atomic.Bool
	Closed    atomic.Bool
	// CloseDelay makes Close slow, like a worker taking time to stop. Set it in OnStart.
	CloseDelay time.Duration

	server *worker.Server
	grpc   *grpc.Server
	cc     *grpc.ClientConn
	client workerv1.WorkerClient
	done   chan struct{}
	once   sync.Once
	onDone func() // called synchronously once the fake has died
}

type fakeService struct {
	*worker.Server
	f *Fake
}

func (s fakeService) Ping(ctx context.Context, req *workerv1.PingRequest) (*workerv1.PingResponse, error) {
	if s.f.PingBlock.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.Server.Ping(ctx, req)
}

func newFake(id string, onDone func()) *Fake {
	f := &Fake{ID: id, WA: &FakeWA{PairCode: "PAIR-CODE"}, done: make(chan struct{}), onDone: onDone}
	f.server = worker.NewServer(f.WA)
	_ = f.server.Start(context.Background())
	lis := bufconn.Listen(1 << 20)
	f.grpc = grpc.NewServer(wire.ServerOptions()...)
	workerv1.RegisterWorkerServer(f.grpc, fakeService{Server: f.server, f: f})
	go f.grpc.Serve(lis)
	f.cc, _ = grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()), wire.DialOption())
	f.client = workerv1.NewWorkerClient(f.cc)
	return f
}

// Emit reports a connection state from the fake WhatsApp connection.
func (f *Fake) Emit(state string) {
	jid := ""
	if state == "connected" {
		jid = testJID
	}
	f.WA.getSink().ConnState(state, "", jid)
}

// EmitConn reports a connection state with a reason.
func (f *Fake) EmitConn(state, reason string) { f.WA.getSink().ConnState(state, reason, "") }

func (f *Fake) EmitQR(code string) { f.WA.getSink().QR(code, time.Minute) }

// EmitMessage, EmitReceipt and EmitHistory simulate data events from WhatsApp.
func (f *Fake) EmitMessage(m *workerv1.Message) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Message{Message: m}})
}

func (f *Fake) EmitReceipt(r *workerv1.Receipt) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Receipt{Receipt: r}})
}

func (f *Fake) EmitHistory(h *workerv1.HistoryChunk) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_History{History: h}})
}

// EmitCall simulates a call signal; offers become rejectable until they end.
func (f *Fake) EmitCall(c *workerv1.Call) {
	f.WA.msgMu.Lock()
	switch c.Kind {
	case "offer", "offer_notice":
		if f.WA.offers == nil {
			f.WA.offers = map[string]bool{}
		}
		f.WA.offers[c.CallId] = true
	case "terminate":
		delete(f.WA.offers, c.CallId)
	case "reject":
		if !c.FromSelf && !c.FromHosted { // our own and hosted devices' rejects do not end the call
			delete(f.WA.offers, c.CallId)
		}
	}
	f.WA.msgMu.Unlock()
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Call{Call: c}})
}

// EmitChatPresence and EmitPresence simulate typing and online/offline updates.
func (f *Fake) EmitChatPresence(p *workerv1.ChatPresence) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_ChatPresence{ChatPresence: p}})
}

func (f *Fake) EmitPresence(p *workerv1.Presence) {
	f.WA.getSink().Data(&workerv1.Event{Kind: &workerv1.Event_Presence{Presence: p}})
}

// Crash simulates the worker process dying.
func (f *Fake) Crash() {
	f.once.Do(func() {
		close(f.done)
		if f.onDone != nil {
			f.onDone()
		}
		f.grpc.Stop()
		f.cc.Close()
	})
}

func (f *Fake) Client() workerv1.WorkerClient { return f.client }
func (f *Fake) Done() <-chan struct{}         { return f.done }
func (f *Fake) Kill() error                   { f.Crash(); return nil }

// Close mimics a graceful stop: in-flight RPCs (e.g. Logout) finish before the fake dies.
func (f *Fake) Close(ctx context.Context) error {
	f.Closed.Store(true)
	if f.CloseDelay > 0 {
		select {
		case <-time.After(f.CloseDelay):
		case <-ctx.Done():
		}
	}
	done := make(chan struct{})
	go func() { f.grpc.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	f.Crash()
	return nil
}

// Spawner is a spawner.Spawner that creates Fakes and records every start.
type Spawner struct {
	mu        sync.Mutex
	fakes     map[string][]*Fake
	live      map[string]int
	maxLive   map[string]int
	OnStart   func(*Fake)
	FailStart error
}

func NewSpawner() *Spawner {
	return &Spawner{fakes: map[string][]*Fake{}, live: map[string]int{}, maxLive: map[string]int{}}
}

func (s *Spawner) Start(ctx context.Context, id string) (spawner.WorkerHandle, error) {
	s.mu.Lock()
	fail, onStart := s.FailStart, s.OnStart
	s.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	f := newFake(id, func() {
		s.mu.Lock()
		s.live[id]--
		s.mu.Unlock()
	})
	s.mu.Lock()
	s.fakes[id] = append(s.fakes[id], f)
	s.live[id]++
	if s.live[id] > s.maxLive[id] {
		s.maxLive[id] = s.live[id]
	}
	s.mu.Unlock()
	if onStart != nil {
		onStart(f)
	}
	return f, nil
}

func (s *Spawner) Starts(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.fakes[id])
}

// Live is the number of fakes for id that have started and not yet died.
func (s *Spawner) Live(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

// MaxLive is the highest Live value ever seen for id.
func (s *Spawner) MaxLive(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxLive[id]
}

func (s *Spawner) Last(id string) *Fake {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.fakes[id]
	if len(l) == 0 {
		return nil
	}
	return l[len(l)-1]
}

func (s *Spawner) Nth(id string, n int) *Fake {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakes[id][n]
}
