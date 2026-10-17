package secure

import (
	"strings"
	"testing"
)

func TestNewIDHasPrefixAndIsUnique(t *testing.T) {
	a, b := NewID("inst_"), NewID("inst_")
	if !strings.HasPrefix(a, "inst_") || len(a) != len("inst_")+16 {
		t.Fatalf("id = %q", a)
	}
	if a == b {
		t.Fatal("ids must differ")
	}
}

func TestInstanceTokenAndHash(t *testing.T) {
	tok := NewInstanceToken()
	if !strings.HasPrefix(tok, "lzi_") || len(tok) < 40 {
		t.Fatalf("token = %q", tok)
	}
	h := HashToken(tok)
	if len(h) != 64 || h != HashToken(tok) || h == HashToken(tok+"x") {
		t.Fatalf("bad hash %q", h)
	}
}

func TestEqual(t *testing.T) {
	if !Equal("abc", "abc") || Equal("abc", "abd") || Equal("", "x") {
		t.Fatal("Equal misbehaves")
	}
}
