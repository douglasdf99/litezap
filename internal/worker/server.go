// Package worker runs one WhatsApp connection and serves it over gRPC to the supervisor.
package worker

import (
	"context"
	"sync"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

// Sink receives connection events from a WAClient.
type Sink interface {
	ConnState(state, reason, jid string)
	QR(code string, timeout time.Duration)
}

// WAClient is the WhatsApp connection used by Server; whatsmeow in production, stubs in tests.
type WAClient interface {
	// Run starts connecting (pairing via QR when there is no session) and returns once started.
	Run(ctx context.Context, sink Sink) error
	PairPhone(ctx context.Context, phone string) (string, error)
	Logout(ctx context.Context) error
	Disconnect()
}

// Server implements workerv1.WorkerServer and fans out WAClient events to subscribers.
type Server struct {
	workerv1.UnimplementedWorkerServer

	wa   WAClient
	mu   sync.Mutex
	conn *workerv1.ConnState
	qr   *workerv1.QR
	subs map[chan *workerv1.Event]struct{}

	done     chan struct{}
	doneOnce sync.Once
}

func NewServer(wa WAClient) *Server {
	return &Server{wa: wa, subs: map[chan *workerv1.Event]struct{}{}, done: make(chan struct{})}
}

// Start runs the WAClient with the server as its sink.
func (s *Server) Start(ctx context.Context) error { return s.wa.Run(ctx, s) }

// Stop disconnects the WAClient and ends all open Events streams.
func (s *Server) Stop() {
	s.doneOnce.Do(func() { close(s.done) })
	s.wa.Disconnect()
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

func (s *Server) QR(code string, timeout time.Duration) {
	q := &workerv1.QR{Code: code, TimeoutMs: timeout.Milliseconds()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn.GetState() == "qr_pending" {
		s.qr = q
	}
	s.broadcast(&workerv1.Event{Kind: &workerv1.Event_Qr{Qr: q}})
}

// broadcast must be called with s.mu held. A full subscriber buffer drops the event;
// the supervisor consumes continuously, and state is re-sent on every change.
func (s *Server) broadcast(ev *workerv1.Event) {
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Server) Events(_ *workerv1.EventsRequest, stream workerv1.Worker_EventsServer) error {
	ch := make(chan *workerv1.Event, 256)
	s.mu.Lock()
	if s.conn != nil {
		ch <- &workerv1.Event{Kind: &workerv1.Event_Conn{Conn: s.conn}}
	}
	if s.qr != nil {
		ch <- &workerv1.Event{Kind: &workerv1.Event_Qr{Qr: s.qr}}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-s.done:
			return nil
		case ev := <-ch:
			if err := stream.Send(ev); err != nil {
				return err
			}
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
