package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func searchIDs(t *testing.T, s *Store, q SearchQuery) []string {
	t.Helper()
	if q.Limit == 0 {
		q.Limit = 50
	}
	hits, _, err := s.SearchMessages(context.Background(), "i1", q)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	return ids
}

// CHT-11: full-text search over text, captions and file names; deletes, revokes and
// edits follow the index through its triggers.
func TestSearchMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	seedInstance(t, s, "i2")
	chat, other := "5511999990000@s.whatsapp.net", "5511999990001@s.whatsapp.net"
	add := func(inst, id, jid, typ, text, media string, at time.Time, fromMe bool) {
		t.Helper()
		m := Message{ID: id, WAMessageID: "W" + id, ChatJID: jid, SenderJID: jid, FromMe: fromMe, Source: "contact",
			Type: typ, Text: text, MediaJSON: media, Status: "received", Timestamp: at}
		if fromMe {
			m.SenderJID = ""
		}
		if _, err := s.InsertMessage(ctx, inst, m, ChatUpdate{}); err != nil {
			t.Fatal(err)
		}
	}
	add("i1", "a", chat, "text", "Quero o orçamento da reforma", "", t0, false)
	add("i1", "b", chat, "image", "foto do Orçamento", `{"kind":"image"}`, t0.Add(time.Minute), false)
	add("i1", "c", other, "document", "", `{"kind":"document","file_name":"orcamento-final.pdf"}`, t0.Add(2*time.Minute), true)
	add("i1", "d", other, "reaction", "orçamento", "", t0.Add(3*time.Minute), false)
	add("i1", "e", other, "text", "nada a ver", "", t0.Add(4*time.Minute), false)
	add("i2", "x", chat, "text", "orçamento de outra instância", "", t0, false)

	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "orcamento"})); got != "[c b a]" {
		t.Fatalf("accents/case/file name: %s", got)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "orça refor"})); got != "[a]" {
		t.Fatalf("prefix AND: %s", got)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "orcamento", Chat: chat, After: t0})); got != "[b]" {
		t.Fatalf("chat+after: %s", got)
	}
	me := true
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "orcamento", FromMe: &me})); got != "[c]" {
		t.Fatalf("from me: %s", got)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "orcamento", Sender: chat, Before: t0.Add(time.Minute)})); got != "[a]" {
		t.Fatalf("sender+before: %s", got)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: `"); DROP TABLE messages; -- OR NEAR(`})); got != "[]" {
		t.Fatalf("operators must be literal: %s", got)
	}
	if got := searchIDs(t, s, SearchQuery{Text: "  ¿?  "}); len(got) != 0 {
		t.Fatalf("no words: %v", got)
	}
	// Paging with the cursor.
	hits, cur, _ := s.SearchMessages(ctx, "i1", SearchQuery{Text: "orcamento", Limit: 2})
	if len(hits) != 2 || hits[0].Snippet == "" {
		t.Fatalf("page 1 = %+v", hits)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "orcamento", Cursor: &cur})); got != "[a]" {
		t.Fatalf("page 2: %s", got)
	}
	// Edits, revokes and deletes follow.
	if _, err := s.EditMessage(ctx, "i1", chat, "Wa", "mudou de assunto", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeMessage(ctx, "i1", chat, "Wb", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "orcamento"})); got != "[c]" {
		t.Fatalf("after edit/revoke: %s", got)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "assunto"})); got != "[a]" {
		t.Fatalf("edited text: %s", got)
	}
	if _, err := s.PruneMessages(ctx, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(t, s, SearchQuery{Text: "orcamento"}); len(got) != 0 {
		t.Fatalf("after retention: %v", got)
	}
	if err := s.DeleteInstance(ctx, "i2"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH '"instancia"*'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("deleted instance still indexed: %d %v", n, err)
	}
}

func TestMatchExpr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"orçamento a", `"orçamento"* "a"`},
		{"e-mail", `"e" "mail"*`},
		{"orçamento", "\"orçamento\"*"}, // a decomposed ç stays in its word
		{"é ok", "\"é\" \"ok\"*"},       // one letter plus its accent: no prefix
		{"  ¿? ", ""},
	}
	for _, c := range cases {
		if got := MatchExpr(c.in); got != c.want {
			t.Errorf("MatchExpr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The search leaves out chats the user deleted, unless asked.
func TestSearchSkipsDeletedChats(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	chat := "5511999990000@s.whatsapp.net"
	for i, id := range []string{"old", "new"} {
		if _, err := s.InsertMessage(ctx, "i1", Message{ID: id, WAMessageID: "W" + id, ChatJID: chat, Source: "contact", Type: "text",
			Text: "boleto " + id, Status: "received", Timestamp: t0.Add(time.Duration(i) * time.Hour)}, ChatUpdate{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.ApplyChatChange(ctx, "i1", chat, ChatChange{Field: "deleted", Value: true, At: t0.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "boleto"})); got != "[new]" {
		t.Fatalf("default = %s", got)
	}
	if got := fmt.Sprint(searchIDs(t, s, SearchQuery{Text: "boleto", IncludeDeleted: true})); got != "[new old]" {
		t.Fatalf("include_deleted = %s", got)
	}
}

// A failing backfill batch is retried with backoff until ctx ends (not abandoned).
func TestSearchBackfillRetries(t *testing.T) {
	s := openTest(t)
	if err := s.SetKV(context.Background(), ftsBackfillKey, "10"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE messages_fts`); err != nil { // every batch now fails
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	fails := 0
	start := time.Now()
	s.BackfillSearchIndex(ctx, 10, time.Millisecond, func(error) { fails++ })
	if fails < 2 || time.Since(start) < 140*time.Millisecond {
		t.Fatalf("failures = %d after %v", fails, time.Since(start))
	}
}

// Messages logged before the index existed are indexed in the background, newest first.
func TestSearchBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := &Store{db: db}
	if err := old.migrateUpTo(ctx, "0019_chat_disappearing.sql"); err != nil {
		t.Fatal(err)
	}
	seedInstance(t, old, "i1")
	for i := range 25 {
		if _, err := old.InsertMessage(ctx, "i1", Message{ID: fmt.Sprintf("m%02d", i), WAMessageID: fmt.Sprintf("W%02d", i),
			ChatJID: "5511999990000@s.whatsapp.net", Source: "contact", Type: "text", Text: fmt.Sprintf("pedido numero %d", i),
			Status: "received", Timestamp: t0.Add(time.Duration(i) * time.Minute)}, ChatUpdate{}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if done, _ := s.SearchIndexComplete(ctx); done {
		t.Fatal("index complete before the backfill")
	}
	// A message edited before the backfill reaches it is indexed once.
	if _, err := s.EditMessage(ctx, "i1", "5511999990000@s.whatsapp.net", "W01", "pedido editado", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if more, err := s.BackfillSearchBatch(ctx, 10); err != nil || !more {
		t.Fatalf("batch 1: %v %v", more, err)
	}
	if got := len(searchIDs(t, s, SearchQuery{Text: "pedido"})); got != 11 {
		t.Fatalf("after batch 1 = %d (newest 10 + edited)", got)
	}
	s.BackfillSearchIndex(ctx, 10, time.Millisecond, func(err error) { t.Errorf("backfill: %v", err) })
	if got := len(searchIDs(t, s, SearchQuery{Text: "pedido"})); got != 25 {
		t.Fatalf("after backfill = %d", got)
	}
	if done, _ := s.SearchIndexComplete(ctx); !done {
		t.Fatal("index not complete")
	}
	if more, err := s.BackfillSearchBatch(ctx, 10); err != nil || more {
		t.Fatalf("idle batch: %v %v", more, err)
	}
	// A fresh database has nothing to backfill.
	if done, _ := openTest(t).SearchIndexComplete(ctx); !done {
		t.Fatal("fresh database waits for a backfill")
	}
}

func TestClearChatMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedInstance(t, s, "i1")
	chat, other := "5511999990000@s.whatsapp.net", "5511999990001@s.whatsapp.net"
	if _, err := s.ClearChatMessages(ctx, "i1", chat, t0, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing chat: %v", err)
	}
	add := func(id, jid, status string, at time.Time) {
		t.Helper()
		if _, err := s.InsertMessage(ctx, "i1", Message{ID: id, WAMessageID: "W" + id, ChatJID: jid, Source: "contact",
			Type: "text", Text: id, Status: status, Timestamp: at}, ChatUpdate{CountUnread: true, Inbound: true}); err != nil {
			t.Fatal(err)
		}
	}
	add("a", chat, "received", t0)
	add("b", chat, "received", t0.Add(time.Minute))
	add("p", chat, "pending", t0.Add(time.Minute)) // still being sent
	add("late", chat, "received", t0.Add(time.Hour))
	add("o", other, "received", t0)
	if _, err := s.SetStarred(ctx, "i1", chat, "Wa", true); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearChatMessages(ctx, "i1", chat, t0.Add(time.Minute), true)
	if err != nil || n != 1 {
		t.Fatalf("clear keeping starred = %d %v", n, err)
	}
	for id, want := range map[string]bool{"a": true, "b": false, "p": true, "late": true, "o": true} {
		if _, err := s.GetMessage(ctx, "i1", id); (err == nil) != want {
			t.Errorf("%s logged = %v, want %v", id, err == nil, want)
		}
	}
	if c, _ := s.GetChat(ctx, "i1", chat); c.UnreadCount != 0 {
		t.Fatalf("unread after clear = %d", c.UnreadCount)
	}
	if n, _ := s.ClearChatMessages(ctx, "i1", chat, t0.Add(time.Hour), false); n != 2 {
		t.Fatalf("clear all = %d (starred and later messages)", n)
	}
}
