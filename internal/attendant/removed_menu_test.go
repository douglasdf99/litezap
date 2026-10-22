package attendant

import (
	"testing"

	"github.com/douglasdf99/litezap/internal/store"
)

// A stored profile still on the removed Cardápio (knowledge_source "menu") loads with
// the typed products instead of failing; a new profile cannot choose it.
func TestDecodeProfileRemovedMenuSource(t *testing.T) {
	p, err := decodeProfile(store.AttendantAgent{Name: "A", ProfileJSON: `{"knowledge_source":" Menu ","mode":"vendas"}`})
	if err != nil {
		t.Fatal(err)
	}
	if p.KnowledgeSource != KnowledgeManual {
		t.Fatalf("knowledge_source = %q, want manual", p.KnowledgeSource)
	}
	in := Profile{Name: "B", KnowledgeSource: "menu"}
	in.normalize()
	if in.Validate() == nil {
		t.Fatal("a new profile may not choose the removed menu source")
	}
}
