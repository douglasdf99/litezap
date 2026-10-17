// Package worker runs one WhatsApp connection and serves it over gRPC to the supervisor.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Sink receives events from a WAClient.
type Sink interface {
	ConnState(state, reason, jid string)
	// QR delivers a pairing code; an empty code means the previous one expired.
	QR(code string, timeout time.Duration)
	// Data delivers a message, receipt or history event. Live events are queued (bounded,
	// never dropping a connection state); history chunks block the caller while
	// historySlots chunks are waiting (backpressure on the history sync).
	Data(ev *workerv1.Event)
}

// WAClient is the WhatsApp connection used by Server; whatsmeow in production, stubs in tests.
type WAClient interface {
	// Run starts connecting (pairing via QR when there is no session) and returns once started.
	Run(ctx context.Context, sink Sink) error
	PairPhone(ctx context.Context, phone string) (string, error)
	Logout(ctx context.Context) error
	Disconnect()
}

// Messenger is implemented by WAClients that can exchange messages. Errors should be gRPC
// statuses following the codes documented in worker.proto.
type Messenger interface {
	Send(ctx context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error)
	SendPresence(ctx context.Context, req *workerv1.SendPresenceRequest) error
	MarkRead(ctx context.Context, req *workerv1.MarkReadRequest) error
	// DownloadMedia writes the attachment to dest and returns its size.
	DownloadMedia(ctx context.Context, kind string, ref []byte, dest string) (int64, error)
	Contacts(ctx context.Context) ([]*workerv1.Contact, error)
	Contact(ctx context.Context, jid string) (*workerv1.Contact, bool, error)
	CheckNumbers(ctx context.Context, phones []string) ([]*workerv1.NumberStatus, error)
}

const (
	// maxLive bounds the live events queued for one subscriber, and the live data events
	// kept while there is none (offline messages arrive right after connecting, before the
	// supervisor subscribes). Beyond it the oldest event that is not a connection state is
	// dropped, logged and counted.
	maxLive = 10000
	// historySlots bounds the history chunks waiting to be sent; more block the producer.
	historySlots = 8
)

type subscriber struct {
	live   []*workerv1.Event
	notify chan struct{} // buffered (1)
}

func (sb *subscriber) wake() {
	select {
	case sb.notify <- struct{}{}:
	default:
	}
}

func isData(ev *workerv1.Event) bool { return ev.GetConn() == nil && ev.GetQr() == nil }

// Server implements workerv1.WorkerServer and fans out WAClient events to subscribers.
type Server struct {
	workerv1.UnimplementedWorkerServer

	wa      WAClient
	log     *slog.Logger
	dropped atomic.Uint64

	mu      sync.Mutex
	conn    *workerv1.ConnState
	qr      *workerv1.QR
	subs    map[*subscriber]struct{}
	backlog []*workerv1.Event // live data events waiting for a subscriber
	history []*workerv1.Event // history chunks waiting to be sent (one subscriber takes each)
	// histSlots holds one token per queued or in-flight history chunk.
	histSlots chan struct{}

	done     chan struct{}
	doneOnce sync.Once
}

func NewServer(wa WAClient) *Server {
	return &Server{
		wa: wa, log: slog.New(slog.DiscardHandler), subs: map[*subscriber]struct{}{},
		histSlots: make(chan struct{}, historySlots), done: make(chan struct{}),
	}
}

// SetLogger sets where dropped events are reported. Call it before Start.
func (s *Server) SetLogger(l *slog.Logger) { s.log = l }

// Dropped counts live events dropped because a queue was full.
func (s *Server) Dropped() uint64 { return s.dropped.Load() }

// Start runs the WAClient with the server as its sink.
func (s *Server) Start(ctx context.Context) error { return s.wa.Run(ctx, s) }

// Stop disconnects the WAClient and ends all open Events streams.
func (s *Server) Stop() {
	s.doneOnce.Do(func() { close(s.done) })
	s.wa.Disconnect()
}

// pushLive appends ev to a live queue, dropping the oldest non-connection event when full.
func (s *Server) pushLive(q []*workerv1.Event, ev *workerv1.Event) []*workerv1.Event {
	return s.capLive(append(q, ev))
}

// capLive trims q to maxLive by dropping its oldest non-connection events (logged, counted).
func (s *Server) capLive(q []*workerv1.Event) []*workerv1.Event {
	for len(q) > maxLive {
		i := 0
		for i < len(q) && q[i].GetConn() != nil {
			i++
		}
		if i == len(q) {
			return q // only connection states: never dropped
		}
		q = append(q[:i:i], q[i+1:]...)
		n := s.dropped.Add(1)
		if n == 1 || n%1000 == 0 {
			s.log.Warn("worker event queue full; dropped oldest events", "dropped_total", n)
		}
	}
	return q
}

func (s *Server) ConnState(state, reason, jid string) {
	c := &workerv1.ConnState{State: state, Reason: reason, Jid: jid}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
	if state != "qr_pending" {
		s.qr = nil
	}
	s.broadcast(&workerv1.Event{Kind: &workerv1.Event_Conn{Conn: c}})
}

// QR caches and broadcasts a pairing code. An empty code means the pairing round
// ended: the cached code is dropped (so snapshots stop replaying an expired code) and
// the empty code is broadcast so subscribers clear theirs too.
func (s *Server) QR(code string, timeout time.Duration) {
	q := &workerv1.QR{Code: code, TimeoutMs: timeout.Milliseconds()}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case code == "":
		s.qr = nil
	case s.conn.GetState() == "qr_pending":
		s.qr = q
	}
	s.broadcast(&workerv1.Event{Kind: &workerv1.Event_Qr{Qr: q}})
}

// Data delivers a data event. A history chunk waits for a free slot (backpressure).
func (s *Server) Data(ev *workerv1.Event) {
	if ev.GetHistory() != nil {
		select {
		case s.histSlots <- struct{}{}:
		case <-s.done:
			return
		}
		s.mu.Lock()
		s.history = append(s.history, ev)
		for sb := range s.subs {
			sb.wake()
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) == 0 {
		s.backlog = s.pushLive(s.backlog, ev)
		return
	}
	s.broadcast(ev)
}

// broadcast must be called with s.mu held.
func (s *Server) broadcast(ev *workerv1.Event) {
	for sb := range s.subs {
		sb.live = s.pushLive(sb.live, ev)
		sb.wake()
	}
}

func (s *Server) Events(_ *workerv1.EventsRequest, stream workerv1.Worker_EventsServer) error {
	sb := &subscriber{notify: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.conn != nil {
		sb.live = append(sb.live, &workerv1.Event{Kind: &workerv1.Event_Conn{Conn: s.conn}})
	}
	if s.qr != nil {
		sb.live = append(sb.live, &workerv1.Event{Kind: &workerv1.Event_Qr{Qr: s.qr}})
	}
	sb.live = s.capLive(append(sb.live, s.backlog...))
	s.backlog = nil
	s.subs[sb] = struct{}{}
	s.mu.Unlock()

	var unsent []*workerv1.Event
	defer func() {
		s.mu.Lock()
		delete(s.subs, sb)
		// Live data events this stream did not send wait for the next subscriber.
		if len(s.subs) == 0 {
			var keep []*workerv1.Event
			for _, ev := range append(unsent, sb.live...) {
				if isData(ev) {
					keep = append(keep, ev)
				}
			}
			s.backlog = s.capLive(append(keep, s.backlog...))
		}
		s.mu.Unlock()
	}()
	for {
		// Live events first; a history chunk only when no live event is waiting.
		s.mu.Lock()
		batch := sb.live
		sb.live = nil
		var hist *workerv1.Event
		if len(batch) == 0 && len(s.history) > 0 {
			hist, s.history = s.history[0], s.history[1:]
		}
		s.mu.Unlock()
		for i, ev := range batch {
			if err := stream.Send(ev); err != nil {
				unsent = batch[i:]
				return err
			}
		}
		if hist != nil {
			if err := stream.Send(hist); err != nil {
				s.mu.Lock()
				s.history = append([]*workerv1.Event{hist}, s.history...) // keeps its slot
				s.mu.Unlock()
				return err
			}
			<-s.histSlots
		}
		if len(batch) > 0 || hist != nil {
			continue
		}
		select {
		case <-stream.Context().Done():
			return nil
		case <-s.done:
			return nil
		case <-sb.notify:
		}
	}
}

func (s *Server) Ping(context.Context, *workerv1.PingRequest) (*workerv1.PingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.conn.GetState()
	if state == "" {
		state = "starting"
	}
	return &workerv1.PingResponse{State: state}, nil
}

func (s *Server) PairPhone(ctx context.Context, req *workerv1.PairPhoneRequest) (*workerv1.PairPhoneResponse, error) {
	code, err := s.wa.PairPhone(ctx, req.Phone)
	if err != nil {
		return nil, err
	}
	return &workerv1.PairPhoneResponse{Code: code}, nil
}

func (s *Server) Logout(ctx context.Context, _ *workerv1.LogoutRequest) (*workerv1.LogoutResponse, error) {
	if err := s.wa.Logout(ctx); err != nil {
		return nil, err
	}
	return &workerv1.LogoutResponse{}, nil
}

func (s *Server) messenger() (Messenger, error) {
	m, ok := s.wa.(Messenger)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "messaging not supported by this worker")
	}
	return m, nil
}

func (s *Server) Send(ctx context.Context, req *workerv1.SendRequest) (*workerv1.SendResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	return m.Send(ctx, req)
}

func (s *Server) SendPresence(ctx context.Context, req *workerv1.SendPresenceRequest) (*workerv1.SendPresenceResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	if err := m.SendPresence(ctx, req); err != nil {
		return nil, err
	}
	return &workerv1.SendPresenceResponse{}, nil
}

func (s *Server) MarkRead(ctx context.Context, req *workerv1.MarkReadRequest) (*workerv1.MarkReadResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	if err := m.MarkRead(ctx, req); err != nil {
		return nil, err
	}
	return &workerv1.MarkReadResponse{}, nil
}

func (s *Server) DownloadMedia(ctx context.Context, req *workerv1.DownloadMediaRequest) (*workerv1.DownloadMediaResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	size, err := m.DownloadMedia(ctx, req.Kind, req.Ref, req.DestPath)
	if err != nil {
		return nil, err
	}
	return &workerv1.DownloadMediaResponse{Size: size}, nil
}

func (s *Server) GetContacts(ctx context.Context, _ *workerv1.GetContactsRequest) (*workerv1.GetContactsResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	list, err := m.Contacts(ctx)
	if err != nil {
		return nil, err
	}
	return &workerv1.GetContactsResponse{Contacts: list}, nil
}

func (s *Server) GetContact(ctx context.Context, req *workerv1.GetContactRequest) (*workerv1.GetContactResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	c, found, err := m.Contact(ctx, req.Jid)
	if err != nil {
		return nil, err
	}
	return &workerv1.GetContactResponse{Contact: c, Found: found}, nil
}

func (s *Server) CheckNumbers(ctx context.Context, req *workerv1.CheckNumbersRequest) (*workerv1.CheckNumbersResponse, error) {
	m, err := s.messenger()
	if err != nil {
		return nil, err
	}
	res, err := m.CheckNumbers(ctx, req.Phones)
	if err != nil {
		return nil, err
	}
	return &workerv1.CheckNumbersResponse{Results: res}, nil
}
