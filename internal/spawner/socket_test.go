package spawner

import "testing"

func TestSocketPathIsShortAndStable(t *testing.T) {
	long := "/Users/someone/Library/Application Support/LiteZap/a/very/long/data/directory/that/keeps/going"
	a := SocketPath(long, "inst_0123456789abcdef")
	if len(a) >= 100 {
		t.Fatalf("socket path too long (%d): %s", len(a), a)
	}
	if a != SocketPath(long, "inst_0123456789abcdef") {
		t.Fatal("not deterministic")
	}
	if a == SocketPath(long, "inst_other") || a == SocketPath("/other", "inst_0123456789abcdef") {
		t.Fatal("collision between instances or data dirs")
	}
}
