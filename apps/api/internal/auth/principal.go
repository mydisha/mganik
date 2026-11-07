// Package auth mendefinisikan identitas pemanggil (Principal) yang dipasang
// middleware autentikasi ke context request, dan aturan otorisasi dasarnya.
//
// Cara Principal dibuat (cookie sesi staff, token device POS, sesi konsumen)
// adalah urusan implementasi Resolver di layer service; package ini hanya
// kontraknya, supaya controller dan service domain bisa diuji dengan
// Principal buatan tanpa login sungguhan.
package auth

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// Kind membedakan jenis pemanggil.
type Kind string

const (
	KindStaff    Kind = "staff"    // user console (sesi cookie)
	KindDevice   Kind = "device"   // perangkat POS (token device)
	KindConsumer Kind = "consumer" // konsumen terverifikasi OTP
)

// Principal adalah identitas yang sudah diautentikasi.
type Principal struct {
	Kind Kind
	// ID adalah users.id (staff), pos_devices.id (device), atau consumers.id.
	ID uuid.UUID
	// ActorID adalah user yang bertanggung jawab: staff itu sendiri, atau
	// kasir yang sedang login di device (bisa uuid.Nil kalau tidak ada).
	ActorID uuid.UUID
	// Permissions adalah kode permission gabungan semua role.
	Permissions map[string]struct{}
	// ScopeAll true kalau minimal satu role ber-scope ALL. Kalau false,
	// LocationIDs adalah satu-satunya lokasi yang boleh disentuh. Mode ini
	// selalu eksplisit; daftar lokasi kosong TIDAK berarti semua lokasi.
	ScopeAll    bool
	LocationIDs []uuid.UUID
}

// Can melaporkan apakah principal memiliki permission perm.
func (p *Principal) Can(perm string) bool {
	if p == nil {
		return false
	}
	_, ok := p.Permissions[perm]
	return ok
}

// CanAccessLocation melaporkan apakah principal boleh menyentuh lokasi id.
func (p *Principal) CanAccessLocation(id uuid.UUID) bool {
	if p == nil {
		return false
	}
	if p.ScopeAll {
		return true
	}
	for _, loc := range p.LocationIDs {
		if loc == id {
			return true
		}
	}
	return false
}

// Resolver membaca kredensial dari request (cookie, header Authorization)
// dan mengembalikan Principal. Mengembalikan (nil, nil) kalau request tidak
// membawa kredensial; error hanya untuk kredensial yang ada tetapi tidak
// valid (kedaluwarsa, dicabut) atau kegagalan sistem.
type Resolver interface {
	Resolve(ctx context.Context, r *http.Request) (*Principal, error)
}

type ctxKey struct{}

// WithPrincipal memasang principal ke context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext mengambil principal dari context (nil kalau anonim).
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
