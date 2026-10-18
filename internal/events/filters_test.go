package events

import (
	"encoding/json"
	"testing"
)

func env(typ string, a Attrs) Envelope { return Envelope{Type: typ, Attrs: a} }

func TestFiltersMatch(t *testing.T) {
	yes, no := true, false
	direct := Attrs{Chat: "5511888887777@s.whatsapp.net", MessageType: "text", HasFromMe: true}
	group := Attrs{Chat: "1203630@g.us", MessageType: "image", HasFromMe: true}
	mine := Attrs{Chat: "5511888887777@s.whatsapp.net", MessageType: "text", HasFromMe: true, FromMe: true}
	status := Attrs{} // instance.status: no fields
	cases := []struct {
		name string
		f    Filters
		a    Attrs
		want bool
	}{
		{"empty", Filters{}, group, true},
		{"direct keeps direct", Filters{Chat: "direct"}, direct, true},
		{"direct drops group", Filters{Chat: "direct"}, group, false},
		{"group drops direct", Filters{Chat: "group"}, direct, false},
		{"chat filter ignores instance events", Filters{Chat: "group", Chats: []string{"x@g.us"}}, status, true},
		{"allowlist hit", Filters{Chats: []string{"1203630@g.us"}}, group, true},
		{"allowlist miss", Filters{Chats: []string{"1203630@g.us"}}, direct, false},
		{"denylist", Filters{ExcludeChats: []string{"1203630@g.us"}}, group, false},
		{"types hit", Filters{MessageTypes: []string{"text"}}, direct, true},
		{"types miss", Filters{MessageTypes: []string{"text"}}, group, false},
		{"types ignore non-message events", Filters{MessageTypes: []string{"text"}}, Attrs{Chat: "1203630@g.us", HasFromMe: true}, true},
		{"from_me only", Filters{FromMe: &yes}, direct, false},
		{"from_me only mine", Filters{FromMe: &yes}, mine, true},
		{"exclude from_me", Filters{FromMe: &no}, mine, false},
		{"from_me ignores events without it", Filters{FromMe: &yes}, status, true},
		{"from_me=false keeps message.status", Filters{FromMe: &no}, MessageStatus{Chat: "5511@s.whatsapp.net"}.EventAttrs(), true},
	}
	for _, c := range cases {
		if got := c.f.Match(env("message.received", c.a)); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestFiltersNormalize(t *testing.T) {
	f, err := Filters{Chats: []string{"+55 (11) 98888-7777", "5511988887777@s.whatsapp.net", "5511988887777:3@s.whatsapp.net"},
		MessageTypes: []string{"text", "text"}}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Chats) != 1 || f.Chats[0] != "5511988887777@s.whatsapp.net" || len(f.MessageTypes) != 1 {
		t.Fatalf("normalized = %+v", f)
	}
	for _, bad := range []Filters{
		{Chat: "private"}, {Chats: []string{"abc"}}, {ExcludeChats: []string{"@g.us"}}, {MessageTypes: []string{"video_call"}},
		{Chats: []string{"abc@foo"}},
	} {
		if _, err := bad.Normalize(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if (Filters{}).JSON() != "" {
		t.Fatal("empty filters must store as empty")
	}
	g, err := ParseFilters(Filters{Chat: "group"}.JSON())
	if err != nil || g.Chat != "group" {
		t.Fatalf("round trip = %+v, %v", g, err)
	}
}

func TestOptInTypesNeedExplicitPattern(t *testing.T) {
	if Match("*", TypeChatPresence) || !Match("*", TypeCallReceived) {
		t.Fatal(`"*" must skip chat.presence only`)
	}
	if !Match("chat.*", TypeChatPresence) || !Match(TypeChatPresence, TypeChatPresence) {
		t.Fatal("explicit patterns must match chat.presence")
	}
	if (Filter{}).match(Envelope{Type: TypeChatPresence}) || !(Filter{}).match(Envelope{Type: TypeMessageReceived}) {
		t.Fatal("an empty filter must skip opt-in types")
	}
	for _, typ := range []string{TypeMessageButton, TypeChatPresence, TypeCallReceived, TypeCallAccepted, TypeCallEnded} {
		if !ValidPattern(typ) {
			t.Errorf("%s not valid", typ)
		}
	}
}

func TestPublishAttachesAttrs(t *testing.T) {
	b := NewBus(nil)
	sub := b.Subscribe(Filter{Types: []string{"*", TypeChatPresence}}, 4)
	b.Publish("i1", TypeMessageReceived, Message{Chat: "1203630@g.us", Type: "image", FromMe: true})
	b.Publish("i1", TypeChatPresence, ChatPresence{Chat: "5511@s.whatsapp.net", State: "composing"})
	e := <-sub.C()
	if e.Attrs.Chat != "1203630@g.us" || e.Attrs.MessageType != "image" || !e.Attrs.FromMe || !e.Attrs.HasFromMe {
		t.Fatalf("attrs = %+v", e.Attrs)
	}
	p := <-sub.C()
	var w struct{ Data ChatPresence }
	if json.Unmarshal(p.JSON, &w) != nil || w.Data.State != "composing" || p.Attrs.MessageType != "" {
		t.Fatalf("presence = %s %+v", p.JSON, p.Attrs)
	}
}
