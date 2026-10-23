package mcp

import (
	"context"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/service"
)

// CHT-11: search_messages reads the instance's log (local and REST backends).
func TestSearchMessagesTool(t *testing.T) {
	for _, m := range allModes {
		t.Run(string(m), func(t *testing.T) {
			e := newEnv(t, nil)
			id, tok := e.connected("Busca")
			f := e.sp.Last(id)
			cs := e.client(m, tok)
			contact := "5511999990000@s.whatsapp.net"
			f.EmitMessage(&workerv1.Message{Id: "W1", Chat: contact, Sender: contact, Type: "text", Text: "Segue o orçamento",
				TimestampMs: time.Now().UnixMilli()})
			eventually(t, "message logged", func() bool {
				p, err := e.msg.SearchMessages(context.Background(), id, service.SearchParams{Q: "orcamento"})
				return err == nil && len(p.Messages) == 1
			})
			var page service.SearchPage
			ok(t, cs, "search_messages", map[string]any{"query": "orçamento", "chat": "5511999990000"}, &page)
			if len(page.Messages) != 1 || page.Messages[0].WAMessageID != "W1" || page.Messages[0].Snippet == "" || !page.IndexComplete {
				t.Fatalf("page = %+v", page)
			}
			ok(t, cs, "search_messages", map[string]any{"query": "orçamento", "from": "me"}, &page)
			if len(page.Messages) != 0 {
				t.Fatalf("from me = %+v", page)
			}
			fails(t, cs, "search_messages", map[string]any{"query": "  "}, "q is required")
		})
	}
}
