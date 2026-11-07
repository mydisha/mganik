// Package clock menyediakan sumber waktu yang bisa diganti di test
// (TTL reservasi, impossible travel, masa tunggu poin, heartbeat POS).
package clock

import (
	"sync"
	"time"
)

// Clock mengembalikan waktu sekarang dalam UTC.
type Clock interface {
	Now() time.Time
}

// Real memakai jam sistem.
type Real struct{}

func (Real) Now() time.Time { return time.Now().UTC() }

// Fake adalah jam yang dikendalikan test. Aman dipakai bersamaan.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

func NewFake(t time.Time) *Fake { return &Fake{now: t.UTC()} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance memajukan jam sebesar d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
