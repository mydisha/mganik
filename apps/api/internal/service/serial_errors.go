package service

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/model"
)

// Error domain serialisasi, verifikasi, dan klaim. Code stabil (snake_case
// bahasa Inggris), detail berbahasa Indonesia.
var (
	ErrSerialInvalidTransition = apperror.New(http.StatusConflict, "invalid_transition",
		"Status tidak sesuai", "Aksi ini tidak berlaku untuk status serial saat ini.")
	ErrSerialNotFound = apperror.New(http.StatusNotFound, "serial_not_found",
		"Serial tidak ditemukan", "Kode serial ini tidak terdaftar. Periksa kodenya atau pindai ulang labelnya.")
	ErrSerialWrongLocation = apperror.New(http.StatusConflict, "serial_wrong_location",
		"Serial di lokasi lain", "Serial ini tercatat di lokasi lain, bukan di lokasi yang dipilih.")
	ErrSerialPackedInParent = apperror.New(http.StatusConflict, "packed_in_parent",
		"Masih di dalam kemasan", "Serial ini masih di dalam kemasan lain (inner box, karton, atau palet). Proses kemasan induknya, atau bongkar kemasan itu dulu.")
	ErrSerialLocationNotFound = apperror.New(http.StatusUnprocessableEntity, "location_not_found",
		"Lokasi tidak ditemukan", "Lokasi ini tidak ada atau sudah diarsipkan.")
	ErrSerialLocationNotOwn = apperror.New(http.StatusUnprocessableEntity, "location_not_own",
		"Bukan lokasi sendiri", "Hasil produksi hanya bisa diterima di gudang, hub, atau toko milik sendiri.")
	ErrSerialLocationVirtual = apperror.New(http.StatusUnprocessableEntity, "location_virtual",
		"Lokasi virtual", "Lokasi virtual (misalnya dalam perjalanan) tidak bisa dipilih sebagai asal atau tujuan.")
	ErrSerialPartyNotFound = apperror.New(http.StatusUnprocessableEntity, "party_not_found",
		"Mitra tidak ditemukan", "Mitra tujuan tidak ada atau sudah diarsipkan.")
	ErrShipmentToPartyRequired = apperror.New(http.StatusUnprocessableEntity, "to_party_required",
		"Mitra tujuan wajib diisi", "Lokasi tujuan belum terhubung ke mitra mana pun. Pilih mitra tujuannya.")
	ErrShipmentSameLocation = apperror.New(http.StatusUnprocessableEntity, "same_location",
		"Asal sama dengan tujuan", "Pilih lokasi tujuan yang berbeda dari lokasi asal pengiriman.")
	ErrSerialProductNotFound = apperror.New(http.StatusUnprocessableEntity, "product_not_found",
		"Produk tidak ditemukan", "Produk ini tidak ada atau sudah diarsipkan.")
	ErrSerialLevelNotFound = apperror.New(http.StatusUnprocessableEntity, "level_not_found",
		"Level kemasan tidak dikenal", "Level kemasan harus salah satu dari unit, inner box, karton, atau palet.")
	ErrSerialProductMismatch = apperror.New(http.StatusUnprocessableEntity, "product_mismatch",
		"Produk tidak cocok", "Produk pada serial yang ditandai berbeda dengan produk batch atau kemasan tujuan.")
	ErrSerialBatchNotFound = apperror.New(http.StatusNotFound, "batch_not_found",
		"Batch tidak ditemukan", "Batch produksi yang dipilih tidak ada.")
	ErrSerialBatchNotActive = apperror.New(http.StatusConflict, "batch_not_active",
		"Batch tidak aktif", "Batch ini sedang ditahan atau sudah ditarik (recall), jadi tidak bisa dipakai.")
	ErrSerialLevelMismatch = apperror.New(http.StatusUnprocessableEntity, "level_mismatch",
		"Level kemasan tidak sesuai", "Kemasan induk harus berlevel lebih tinggi dari isinya dan sesuai spesifikasi kemasan produk.")
	ErrSerialMixedLevels = apperror.New(http.StatusUnprocessableEntity, "mixed_levels",
		"Level isi berbeda", "Semua isi dalam satu kemasan harus berlevel sama, misalnya semuanya unit atau semuanya karton.")
	ErrSerialPackagingSpecMissing = apperror.New(http.StatusUnprocessableEntity, "packaging_spec_missing",
		"Spesifikasi kemasan belum ada", "Produk ini belum punya spesifikasi kemasan untuk level tersebut. Tambahkan dulu di data katalog.")
	ErrSerialCapacityExceeded = apperror.New(http.StatusConflict, "capacity_exceeded",
		"Kemasan penuh", "Jumlah isi melebihi kapasitas kemasan induk.")
	ErrSerialMixedBatch = apperror.New(http.StatusUnprocessableEntity, "mixed_batch_not_allowed",
		"Batch tidak boleh dicampur", "Spesifikasi kemasan produk ini tidak mengizinkan isi dari batch berbeda dalam satu kemasan.")
	ErrSerialAlreadyPacked = apperror.New(http.StatusConflict, "already_packed",
		"Sudah di kemasan lain", "Serial ini sudah berada di kemasan lain. Bongkar kemasan itu dulu sebelum memindahkannya.")
	ErrSerialCycleDetected = apperror.New(http.StatusUnprocessableEntity, "cycle_detected",
		"Susunan kemasan tidak valid", "Kemasan tidak bisa dimasukkan ke dirinya sendiri atau ke kemasan yang ada di dalamnya.")
	ErrSerialStatusMismatch = apperror.New(http.StatusConflict, "status_mismatch",
		"Status serial tidak sesuai", "Untuk dikemas atau dibongkar, kemasan dan isinya harus berstatus sama dan berada di lokasi yang sama.")
	ErrSerialEmptyContainer = apperror.New(http.StatusUnprocessableEntity, "empty_container",
		"Kemasan kosong", "Kemasan ini tidak berisi unit, jadi tidak bisa dikirim.")
	ErrShipmentNotFound = apperror.New(http.StatusNotFound, "shipment_not_found",
		"Pengiriman tidak ditemukan", "Pengiriman yang diminta tidak ada.")
	ErrShipmentNotShipped = apperror.New(http.StatusConflict, "shipment_not_shipped",
		"Tidak bisa diterima", "Hanya pengiriman yang sedang dalam perjalanan yang bisa diterima.")
	ErrSerialConcurrentChange = apperror.New(http.StatusConflict, "concurrent_change",
		"Data berubah", "Serial ini diubah oleh proses lain pada saat yang sama. Muat ulang data, lalu coba lagi.")
	ErrSerialDuplicateCode = apperror.New(http.StatusUnprocessableEntity, "duplicate_code",
		"Kode ganda", "Ada kode yang dimasukkan lebih dari sekali. Hapus kode gandanya, lalu kirim lagi.")
	ErrSerialNotUnit = apperror.New(http.StatusUnprocessableEntity, "serial_not_unit",
		"Bukan serial unit", "Hanya serial unit yang bisa dijual. Bongkar kemasannya dulu (inner box, karton, atau palet), lalu pindai unitnya.")
	ErrShipmentToPartyMismatch = apperror.New(http.StatusUnprocessableEntity, "to_party_mismatch",
		"Mitra tujuan tidak cocok", "Lokasi tujuan milik sendiri, jadi mitra tujuan harus pemilik lokasi itu. Kosongkan mitra tujuan agar terisi otomatis.")
	// Konsumen. Pesan klaim tidak menyebut status internal unit.
	ErrClaimPreviouslyRejected = apperror.New(http.StatusForbidden, "claim_previously_rejected",
		"Klaim tidak bisa diulang", "Klaim poin dari akun ini untuk produk ini pernah ditolak. Hubungi layanan pelanggan kalau menurut Anda keputusan itu keliru.")
	ErrClaimReasonRequired = apperror.Validation(apperror.FieldError{
		Field: "note", Code: "required", Message: "wajib diisi untuk membatalkan klaim yang sudah disetujui (REVERSE)"})

	// Sengaja generik: tidak membedakan kode tidak terdaftar dari PIN salah.
	ErrClaimInvalidCodeOrPIN = apperror.New(http.StatusUnprocessableEntity, "invalid_code_or_pin",
		"Kode atau PIN salah", "Periksa lagi kode produk dan PIN di dalam kemasan, lalu coba lagi.")
	ErrClaimLocked = apperror.New(http.StatusLocked, "claim_locked",
		"Klaim dikunci", "PIN untuk produk ini sudah terlalu sering salah, jadi klaimnya dikunci. Hubungi layanan pelanggan.")
	ErrClaimAlreadyClaimed = apperror.New(http.StatusConflict, "already_claimed",
		"Sudah diklaim", "Poin untuk produk ini sudah diklaim. Satu produk hanya bisa diklaim sekali.")
	ErrClaimNotAllowed = apperror.New(http.StatusConflict, "claim_not_allowed",
		"Klaim belum bisa diproses", "Penjualan produk ini belum tercatat di sistem kami. Simpan struk pembelian, lalu hubungi layanan pelanggan.")
	ErrClaimConsumerInactive = apperror.New(http.StatusForbidden, "consumer_inactive",
		"Akun tidak aktif", "Akun ini tidak bisa dipakai. Hubungi layanan pelanggan.")
	// Staff (review klaim).
	ErrClaimNotFound = apperror.New(http.StatusNotFound, "claim_not_found",
		"Klaim tidak ditemukan", "Klaim yang diminta tidak ada.")
	ErrClaimAlreadyDecided = apperror.New(http.StatusConflict, "claim_already_decided",
		"Klaim sudah diputuskan", "Klaim ini sudah disetujui atau ditolak. Muat ulang untuk melihat keputusan terbaru.")
)

// serialMaxErrorFields membatasi daftar field per error supaya respons tetap
// kecil untuk request berisi ribuan kode.
const serialMaxErrorFields = 50

// serialErrWithFields menyalin error domain dan menambahkan daftar field bermasalah.
func serialErrWithFields(base *apperror.Error, fields []apperror.FieldError) *apperror.Error {
	e := *base
	if len(fields) > serialMaxErrorFields {
		fields = fields[:serialMaxErrorFields]
	}
	e.Fields = fields
	return &e
}

// serialErrWithDetail menyalin error domain dengan detail lain.
func serialErrWithDetail(base *apperror.Error, format string, args ...any) *apperror.Error {
	e := *base
	e.Detail = fmt.Sprintf(format, args...)
	return &e
}

// RejectDuplicateCodes mengembalikan 422 duplicate_code (field menunjuk
// kemunculan kedua dst.) kalau codes memuat kode yang sama lebih dari sekali.
// Dipanggil controller dan juga di awal setiap method service yang menerima
// daftar kode, karena modul lain (POS) memanggil service langsung.
func RejectDuplicateCodes(field string, codes []string) error {
	seen := make(map[string]int, len(codes))
	var fields []apperror.FieldError
	for i, c := range codes {
		if first, dup := seen[c]; dup {
			fields = append(fields, apperror.FieldError{Field: fmt.Sprintf("%s[%d]", field, i), Code: "duplicate_code",
				Message: fmt.Sprintf("sama dengan kode ke-%d", first+1)})
			continue
		}
		seen[c] = i
	}
	if len(fields) > 0 {
		return serialErrWithFields(ErrSerialDuplicateCode, fields)
	}
	return nil
}

// serialCodeIndex memetakan kode ke posisi di request untuk nama field error.
type serialCodeIndex struct {
	field string
	pos   map[string]int
}

func newSerialCodeIndex(field string, codes []string) serialCodeIndex {
	pos := make(map[string]int, len(codes))
	for i, c := range codes {
		if _, dup := pos[c]; !dup {
			pos[c] = i
		}
	}
	return serialCodeIndex{field: field, pos: pos}
}

func (ci serialCodeIndex) fieldError(code, errCode, msg string) apperror.FieldError {
	name := ci.field
	if i, ok := ci.pos[code]; ok {
		name = fmt.Sprintf("%s[%d]", ci.field, i)
	}
	return apperror.FieldError{Field: name, Code: errCode, Message: msg}
}

// SerialActor adalah pelaku yang dicatat di unit_events/audit_logs.
type SerialActor struct {
	Type     string
	UserID   *uuid.UUID
	DeviceID *uuid.UUID
}

// SerialSystemActor: aksi dari sistem (job, worker).
var SerialSystemActor = SerialActor{Type: model.ActorSystem}

// serialActorOf menurunkan pelaku dari principal. nil = sistem.
func serialActorOf(p *auth.Principal) SerialActor {
	if p == nil {
		return SerialSystemActor
	}
	switch p.Kind {
	case auth.KindDevice:
		a := SerialActor{Type: model.ActorDevice, DeviceID: serialUUIDPtr(p.ID)}
		if p.ActorID != uuid.Nil {
			a.UserID = serialUUIDPtr(p.ActorID)
		}
		return a
	case auth.KindConsumer:
		return SerialActor{Type: model.ActorConsumer, UserID: serialUUIDPtr(p.ID)}
	default:
		return SerialActor{Type: model.ActorUser, UserID: serialUUIDPtr(p.ID)}
	}
}

func serialUUIDPtr(id uuid.UUID) *uuid.UUID { return &id }

func serialPtr[T any](v T) *T { return &v }
