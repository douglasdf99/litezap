package autoread

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/settings"
	"google.golang.org/grpc"
)

type reads struct {
	workerv1.WorkerClient // only MarkRead is used
	mu                    sync.Mutex
	got                   []*workerv1.MarkReadRequest
	at                    []time.Time
	reset                 []string
}

func (r *reads) MarkRead(_ context.Context, req *workerv1.MarkReadRequest, _ ...grpc.CallOption) (*workerv1.MarkReadResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, req)
	r.at = append(r.at, time.Now())
	return &workerv1.MarkReadResponse{}, nil
}

func (r *reads) Client(string) (workerv1.WorkerClient, error) { return r, nil }

func (r *reads) ResetUnread(_ context.Context, _, chat string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reset = append(r.reset, chat)
	return nil
}

func (r *reads) snapshot() ([]*workerv1.MarkReadRequest, []time.Time, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*workerv1.MarkReadRequest(nil), r.got...), append([]time.Time(nil), r.at...), append([]string(nil), r.reset...)
}

func msg(id, chat, sender string, group bool, typ string) *workerv1.Event {
	return &workerv1.Event{Kind: &workerv1.Event_Message{Message: &workerv1.Message{
		Id: id, Chat: chat, Sender: sender, IsGroup: group, Type: typ}}}
}

func newReader(r *reads, set settings.AutoRead) *Reader {
	return &Reader{Workers: r, Unread: r, MinDelay: 30 * time.Millisecond, MaxDelay: 40 * time.Millisecond, Gap: 50 * time.Millisecond,
		Settings: func(context.Context, string) (settings.Settings, error) {
			s := settings.Defaults()
			s.AutoRead = set
			return s, nil
		}}
}

func waitReads(t *testing.T, r *reads, n int) []*workerv1.MarkReadRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _, _ := r.snapshot(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _, _ := r.snapshot()
	t.Fatalf("reads = %d, want %d", len(got), n)
	return nil
}

func TestAutoReadBatchesAndPaces(t *testing.T) {
	r := &reads{}
	ar := newReader(r, settings.AutoRead{Enabled: true})
	defer ar.Close()
	dm := "5511888887777@s.whatsapp.net"
	ar.WorkerEvent("i1", msg("A1", dm, dm, false, "text"))
	ar.WorkerEvent("i1", msg("A2", dm, dm, false, "image"))
	ar.WorkerEvent("i1", msg("R1", dm, dm, false, "reaction"))                                                                                       // no receipt
	ar.WorkerEvent("i1", &workerv1.Event{Kind: &workerv1.Event_Message{Message: &workerv1.Message{Id: "M1", Chat: dm, FromMe: true, Type: "text"}}}) // ours
	ar.WorkerEvent("i1", msg("G1", "1203@g.us", "5511777@s.whatsapp.net", true, "text"))                                                             // groups off
	ar.WorkerEvent("i1", msg("B1", "5511666665555@s.whatsapp.net", "5511666665555@s.whatsapp.net", false, "text"))
	got := waitReads(t, r, 2)
	time.Sleep(80 * time.Millisecond)
	got, at, reset := r.snapshot()
	if len(got) != 2 {
		t.Fatalf("reads = %+v", got)
	}
	byChat := map[string][]string{}
	for _, g := range got {
		byChat[g.Chat] = g.MessageIds
	}
	if ids := byChat[dm]; len(ids) != 2 || ids[0] != "A1" || ids[1] != "A2" {
		t.Fatalf("batch = %v", byChat)
	}
	if gap := at[1].Sub(at[0]); gap < 45*time.Millisecond {
		t.Fatalf("receipts %v apart, want >= gap", gap)
	}
	sort.Strings(reset)
	if len(reset) != 2 {
		t.Fatalf("unread reset = %v", reset)
	}
}

func TestAutoReadGroupsAndOff(t *testing.T) {
	r := &reads{}
	ar := newReader(r, settings.AutoRead{Enabled: true, Groups: true})
	ar.WorkerEvent("i1", msg("G1", "1203@g.us", "5511777@s.whatsapp.net", true, "text"))
	ar.WorkerEvent("i1", msg("G2", "1203@g.us", "5511888@s.whatsapp.net", true, "text"))
	got := waitReads(t, r, 2)
	for _, g := range got {
		if g.Sender == "" || len(g.MessageIds) != 1 {
			t.Fatalf("group receipts are per sender: %+v", got)
		}
	}
	ar.Close()

	off := &reads{}
	ar = newReader(off, settings.AutoRead{})
	ar.WorkerEvent("i1", msg("A1", "5511888887777@s.whatsapp.net", "", false, "text"))
	time.Sleep(100 * time.Millisecond)
	if got, _, _ := off.snapshot(); len(got) != 0 {
		t.Fatalf("auto_read off still read: %+v", got)
	}
	ar.Close()
}

// Close drops pending batches: nothing is read after it.
func TestAutoReadClose(t *testing.T) {
	r := &reads{}
	ar := newReader(r, settings.AutoRead{Enabled: true})
	ar.WorkerEvent("i1", msg("A1", "5511888887777@s.whatsapp.net", "", false, "text"))
	ar.Close()
	ar.WorkerEvent("i1", msg("A2", "5511888887777@s.whatsapp.net", "", false, "text"))
	time.Sleep(100 * time.Millisecond)
	if got, _, _ := r.snapshot(); len(got) != 0 {
		t.Fatalf("read after Close: %+v", got)
	}
}
