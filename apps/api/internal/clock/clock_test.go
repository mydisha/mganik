package clock

import (
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	start := time.Date(2026, 11, 7, 10, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	f := NewFake(start)
	if !f.Now().Equal(start) || f.Now().Location() != time.UTC {
		t.Fatalf("Now %v harus sama dengan start dalam UTC", f.Now())
	}
	f.Advance(90 * time.Minute)
	if got := f.Now().Sub(start); got != 90*time.Minute {
		t.Fatalf("maju %s, ingin 90m", got)
	}
	if (Real{}).Now().Location() != time.UTC {
		t.Fatal("Real harus UTC")
	}
}
