package route

import "testing"

func TestBoundUnderLimitIsUnchanged(t *testing.T) {
	if got := Bound("short", 100); got != "short" {
		t.Errorf("got %q", got)
	}
}

func TestBoundZeroOrNegativeIsUnbounded(t *testing.T) {
	long := make([]byte, 10000)
	for i := range long {
		long[i] = 'x'
	}
	if got := Bound(string(long), 0); got != string(long) {
		t.Error("maxBytes<=0 should be unbounded")
	}
	if got := Bound(string(long), -1); got != string(long) {
		t.Error("maxBytes<=0 should be unbounded")
	}
}

func TestBoundKeepsBothEndsIntact(t *testing.T) {
	head := "HEAD-START-MARKER"
	tail := "TAIL-END-MARKER"
	middle := make([]byte, 5000)
	for i := range middle {
		middle[i] = 'm'
	}
	text := head + string(middle) + tail
	got := Bound(text, 200)
	if len(got) > 200+64 { // allow the marker's own overhead
		t.Errorf("len(got) = %d, expected roughly bounded to 200", len(got))
	}
	if len(got) >= len(text) {
		t.Fatalf("expected truncation, got len %d vs original %d", len(got), len(text))
	}
	if got[:len(head)] != head {
		t.Errorf("head not intact: %q", got[:len(head)])
	}
	if got[len(got)-len(tail):] != tail {
		t.Errorf("tail not intact: %q", got[len(got)-len(tail):])
	}
}

func TestBoundNeverSplitsARune(t *testing.T) {
	// A multi-byte rune ("é", 2 bytes in UTF-8) straddling a would-be cut.
	text := "abc" + "é" + string(make([]byte, 1000)) + "é" + "xyz"
	got := Bound(text, 10)
	for i := 0; i < len(got); {
		r := got[i]
		switch {
		case r < 0x80:
			i++
		case r&0xE0 == 0xC0:
			i += 2
		default:
			t.Fatalf("unexpected leading byte 0x%x at %d in %q", r, i, got)
		}
		if i > len(got) {
			t.Fatalf("rune boundary overruns string: %q", got)
		}
	}
}
