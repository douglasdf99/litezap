// Package workertest provides an in-memory worker and spawner for supervisor tests.
package workertest

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const testJID = "5511999999999:7@s.whatsapp.net"

// FakeWA is a scriptable worker.WAClient.
type FakeWA struct {
	mu       sync.Mutex
	sink     worker.Sink
	PairCode string
	Logouts  atomic.Int32
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
	f.getSink().ConnState("logged_out", "api", "")
	return nil
}

func (f *FakeWA) Disconnect() {}

// Fake is one in-memory worker; it implements spawner.WorkerHandle.
type Fake struct {
	ID        string
	WA        *FakeWA
	PingBlock atomic.Bool
	Closed    atomic.Bool

	server *worker.Server
	grpc   *grpc.Server
	cc     *grpc.ClientConn
	client workerv1.WorkerClient
	done   chan struct{}
	once   sync.Once
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

func newFake(id string) *Fake {
	f := &Fake{ID: id, WA: &FakeWA{PairCode: "PAIR-CODE"}, done: make(chan struct{})}
	f.server = worker.NewServer(f.WA)
	_ = f.server.Start(context.Background())
	lis := bufconn.Listen(1 << 20)
	f.grpc = grpc.NewServer()
	workerv1.RegisterWorkerServer(f.grpc, fakeService{Server: f.server, f: f})
	go f.grpc.Serve(lis)
	f.cc, _ = grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
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

func (f *Fake) EmitQR(code string) { f.WA.getSink().QR(code, time.Minute) }

// Crash simulates the worker process dying.
func (f *Fake) Crash() {
	f.once.Do(func() {
		close(f.done)
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
	OnStart   func(*Fake)
	FailStart error
}

func NewSpawner() *Spawner { return &Spawner{fakes: map[string][]*Fake{}} }

func (s *Spawner) Start(ctx context.Context, id string) (spawner.WorkerHandle, error) {
	s.mu.Lock()
	fail, onStart := s.FailStart, s.OnStart
	s.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	f := newFake(id)
	s.mu.Lock()
	s.fakes[id] = append(s.fakes[id], f)
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
