package dto

import "github.com/google/uuid"

// LoginRequest: POST /auth/login. Batas panjang mencegah input raksasa ke
// argon2id; format email tidak divalidasi ketat (email tidak dikenal = gagal
// generik, supaya pesan tidak membedakan).
type LoginRequest struct {
	Email    string `json:"email" binding:"required,max=254"`
	Password string `json:"password" binding:"required,max=1024"`
}

// LocationRef: referensi lokasi singkat.
type LocationRef struct {
	ID   uuid.UUID `json:"id"`
	Code string    `json:"code"`
	Name string    `json:"name"`
}

// MeUser: identitas staff.
type MeUser struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
	Name  string    `json:"name"`
}

// MeRole: satu role beserta scope-nya. Locations hanya terisi untuk SELECTED.
type MeRole struct {
	Code      string        `json:"code"`
	Name      string        `json:"name"`
	ScopeMode string        `json:"scope_mode"`
	Locations []LocationRef `json:"locations"`
}

// MeScope: scope lokasi efektif. Mode ALL = semua lokasi (Locations kosong);
// SELECTED = hanya Locations (kosong berarti tidak ada akses lokasi).
type MeScope struct {
	Mode      string        `json:"mode"`
	Locations []LocationRef `json:"locations"`
}

// MeResponse: GET /auth/me dan body sukses POST /auth/login.
type MeResponse struct {
	User        MeUser   `json:"user"`
	Roles       []MeRole `json:"roles"`
	Permissions []string `json:"permissions"`
	Scope       MeScope  `json:"scope"`
}
