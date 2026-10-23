package ingest

import (
	"context"
	"crypto/sha256"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/events"
	"github.com/douglasdf99/litezap/internal/store"
)

// MSG-23: received votes are kept on the poll (the latest per voter, ours as "me") with
// the results recounted, and still published as message.poll_vote.
func TestPollVotesPersisted(t *testing.T) {
	in, st := setup(t)
	pub := &recPub{}
	in.Events = pub
	ctx := context.Background()
	const group = "120363000000000009@g.us"
	const ana, bia = "5500000000001@s.whatsapp.net", "5500000000002@s.whatsapp.net"
	hash := func(o string) []byte { h := sha256.Sum256([]byte(o)); return h[:] }
	vote := func(id, sender string, fromMe bool, at int64, opts ...string) {
		m := &workerv1.Message{Id: id, Chat: group, Sender: sender, FromMe: fromMe, IsGroup: true, TimestampMs: at,
			Type: "poll_vote", TargetId: "POLLG"}
		for _, o := range opts {
			m.PollHashes = append(m.PollHashes, hash(o))
		}
		in.WorkerEvent("i1", msgEv(m))
	}
	in.WorkerEvent("i1", msgEv(&workerv1.Message{Id: "POLLG", Chat: group, Sender: ana, IsGroup: true, TimestampMs: ts, Type: "poll",
		Text: "Pizza?", Poll: &workerv1.Poll{Name: "Pizza?", Options: []string{"calabresa", "marguerita", "atum"}}}))
	vote("V1", ana, false, ts+1000, "calabresa")
	vote("V2", bia, false, ts+2000, "calabresa", "atum")
	vote("V3", ana, false, ts+3000, "marguerita")                     // Ana changes her vote
	vote("V4", ana, false, ts+500, "atum")                            // an older vote arriving late is ignored
	vote("V5", "5500000000009@s.whatsapp.net", true, ts+4000, "atum") // our vote from the phone
	vote("V6", "5500000000009@s.whatsapp.net", true, ts+5000, "marguerita")
	vote("V7", bia, false, ts+6000) // Bia removes her vote

	poll, err := st.MessageByWAID(ctx, "i1", group, "POLLG")
	if err != nil {
		t.Fatal(err)
	}
	p := store.ParseMeta(poll.MetaJSON).Poll
	if p == nil || len(p.Votes) != 3 {
		t.Fatalf("poll = %s", poll.MetaJSON)
	}
	if v := p.Votes[ana]; len(v.Options) != 1 || v.Options[0] != "marguerita" || v.At.UnixMilli() != ts+3000 {
		t.Fatalf("ana = %+v", v)
	}
	if v := p.Votes["me"]; len(v.Options) != 1 || v.Options[0] != "marguerita" {
		t.Fatalf("me = %+v (our votes from any device count once)", v)
	}
	if v, ok := p.Votes[bia]; !ok || v.Options == nil || len(v.Options) != 0 {
		t.Fatalf("bia = %+v, %v (a removed vote stays, empty)", v, ok)
	}
	if r := p.Results; r["marguerita"] != 2 || r["calabresa"] != 0 || r["atum"] != 0 || len(r) != 3 {
		t.Fatalf("results = %v", r)
	}
	// Every vote is still published, with the voter normalized like the poll's keys.
	evs := pub.snapshot()
	var votes []events.PollVote
	for _, e := range evs {
		if e.typ == events.TypeMessagePollVote {
			votes = append(votes, e.data.(events.PollVote))
		}
	}
	if len(votes) != 7 || votes[4].Voter != "me" || !votes[4].FromMe || votes[0].Voter != ana || votes[0].PollID != poll.ID {
		t.Fatalf("published = %+v", votes)
	}
	// The log only holds the poll: votes are not messages.
	if list, _, _ := st.ListMessages(ctx, "i1", group, 10, nil); len(list) != 1 {
		t.Fatalf("log = %+v", list)
	}
}
