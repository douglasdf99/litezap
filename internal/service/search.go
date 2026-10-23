package service

import (
	"context"
	"strings"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/store"
)

// SearchParams is a full-text search over an instance's message log (CHT-11). Q is
// required; the other fields narrow it.
type SearchParams struct {
	Q      string `json:"q"`
	Chat   string `json:"chat,omitempty"`   // chat JID or phone number
	From   string `json:"from,omitempty"`   // "me", or the sender's JID or phone number
	Before string `json:"before,omitempty"` // RFC 3339: only older messages
	After  string `json:"after,omitempty"`  // RFC 3339: only newer messages
	Limit  int    `json:"limit,omitempty"`  // default 50, max 200
	Cursor string `json:"cursor,omitempty"`
	// IncludeDeleted also searches chats the user deleted (left out by default).
	IncludeDeleted bool `json:"include_deleted,omitempty"`
}

// SearchHitView is a message that matches a search, with a short excerpt around the
// match (the matched words between « and »).
type SearchHitView struct {
	MessageView
	Snippet string `json:"snippet,omitempty"`
}

// SearchPage is a page of search results, newest first.
type SearchPage struct {
	Messages   []SearchHitView `json:"messages"`
	NextCursor string          `json:"next_cursor,omitempty"`
	// IndexComplete is false while messages logged before the search index existed are
	// still being indexed (older results may be missing).
	IndexComplete bool `json:"index_complete"`
}

const maxSearchQuery = 200

func searchTime(name, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, apierr.Validation(name + " must be an RFC 3339 time, e.g. 2026-10-23T00:00:00Z")
	}
	return t, nil
}

// SearchMessages searches the message log of an instance: text, captions and document
// file names, every word as a prefix, accents and case ignored. Revoked messages and
// reactions are not returned. It only reads LiteZap's log (nothing reaches WhatsApp).
func (s *Messaging) SearchMessages(ctx context.Context, instanceID string, p SearchParams) (SearchPage, error) {
	if _, err := s.Store.GetInstance(ctx, instanceID); err != nil {
		return SearchPage{}, mapErr(err)
	}
	q := strings.TrimSpace(p.Q)
	switch {
	case q == "":
		return SearchPage{}, apierr.Validation("q is required")
	case len(q) > maxSearchQuery:
		return SearchPage{}, apierr.Validation("q is limited to 200 characters")
	case store.MatchExpr(q) == "":
		return SearchPage{}, apierr.Validation("q needs at least one word (letters or digits)")
	}
	sq := store.SearchQuery{Text: q, Limit: pageLimit(p.Limit), IncludeDeleted: p.IncludeDeleted}
	var err error
	if p.Chat != "" {
		if sq.Chat, err = ChatJID(p.Chat); err != nil {
			return SearchPage{}, err
		}
	}
	switch from := strings.TrimSpace(p.From); {
	case from == "":
	case strings.EqualFold(from, "me"):
		me := true
		sq.FromMe = &me
	default:
		if sq.Sender, err = ChatJID(from); err != nil || strings.HasSuffix(sq.Sender, "@g.us") {
			return SearchPage{}, apierr.Validation(`from must be "me", a phone number or a contact JID`)
		}
	}
	if sq.Before, err = searchTime("before", p.Before); err != nil {
		return SearchPage{}, err
	}
	if sq.After, err = searchTime("after", p.After); err != nil {
		return SearchPage{}, err
	}
	if p.Cursor != "" {
		sq.Cursor = &store.MessageCursor{}
		if err := decodeCursor(p.Cursor, sq.Cursor); err != nil {
			return SearchPage{}, err
		}
	}
	hits, cur, err := s.Store.SearchMessages(ctx, instanceID, sq)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Messages: make([]SearchHitView, 0, len(hits))}
	for _, h := range hits {
		page.Messages = append(page.Messages, SearchHitView{MessageView: messageView(instanceID, h.Message), Snippet: h.Snippet})
	}
	if len(hits) == sq.Limit {
		page.NextCursor = encodeCursor(cur)
	}
	page.IndexComplete, err = s.Store.SearchIndexComplete(ctx)
	return page, err
}
