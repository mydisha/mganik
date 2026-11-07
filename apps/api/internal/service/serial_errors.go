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
		"Transisi status tidak diizinkan", "Status serial tidak bisa dipindah ke status tujuan.")
	ErrSerialNotFound = apperror.New(http.StatusNotFound, "serial_not_found",
		"Serial tidak ditemukan", "Kode serial tidak terdaftar.")
	ErrSerialWrongLocation = apperror.New(http.StatusConflict, "serial_wrong_location",
		"Serial di lokasi lain", "Serial tidak berada di lokasi yang diminta.")
	ErrSerialPackedInParent = apperror.New(http.StatusConflict, "packed_in_parent",
		"Serial masih di dalam kemasan induk", "Kirim/terima kemasan induknya, atau bongkar dulu kemasan induknya.")
	ErrSerialLocationNotFound = apperror.New(http.StatusUnprocessableEntity, "location_not_found",
		"Lokasi tidak ditemukan", "Lokasi tidak ada atau sudah diarsipkan.")
	ErrSerialLocationNotOwn = apperror.New(http.StatusUnprocessableEntity, "location_not_own",
		"Lokasi bukan milik sendiri", "Penerimaan hasil produksi hanya ke lokasi fisik milik sendiri.")
	ErrSerialLocationVirtual = apperror.New(http.StatusUnprocessableEntity, "location_virtual",
		"Lokasi virtual", "Lokasi virtual tidak bisa menjadi asal atau tujuan.")
	ErrSerialPartyNotFound = apperror.New(http.StatusUnprocessableEntity, "party_not_found",
		"Mitra tidak ditemukan", "Party tujuan tidak ada atau sudah diarsipkan.")
	ErrShipmentToPartyRequired = apperror.New(http.StatusUnprocessableEntity, "to_party_required",
		"Mitra tujuan wajib diisi", "Lokasi tujuan tidak terikat ke party; isi to_party_id.")
	ErrShipmentSameLocation = apperror.New(http.StatusUnprocessableEntity, "same_location",
		"Lokasi asal sama dengan tujuan", "Lokasi asal dan tujuan shipment harus berbeda.")
	ErrSerialProductNotFound = apperror.New(http.StatusUnprocessableEntity, "product_not_found",
		"Produk tidak ditemukan", "Produk tidak ada atau sudah diarsipkan.")
	ErrSerialLevelNotFound = apperror.New(http.StatusUnprocessableEntity, "level_not_found",
		"Level kemasan tidak dikenal", "Level kemasan tidak ada di packaging_levels.")
	ErrSerialProductMismatch = apperror.New(http.StatusUnprocessableEntity, "product_mismatch",
		"Produk tidak cocok", "Produk serial tidak sama dengan produk tujuan.")
	ErrSerialBatchNotFound = apperror.New(http.StatusNotFound, "batch_not_found",
		"Batch tidak ditemukan", "Batch produksi tidak ada.")
	ErrSerialBatchNotActive = apperror.New(http.StatusConflict, "batch_not_active",
		"Batch tidak aktif", "Batch sedang ditahan atau sudah di-recall.")
	ErrSerialLevelMismatch = apperror.New(http.StatusUnprocessableEntity, "level_mismatch",
		"Level kemasan tidak sesuai", "Induk harus berlevel lebih tinggi dan sesuai spesifikasi kemasan produk.")
	ErrSerialMixedLevels = apperror.New(http.StatusUnprocessableEntity, "mixed_levels",
		"Level anak berbeda-beda", "Semua anak dalam satu agregasi harus berlevel sama.")
	ErrSerialPackagingSpecMissing = apperror.New(http.StatusUnprocessableEntity, "packaging_spec_missing",
		"Spesifikasi kemasan tidak ada", "Produk belum punya spesifikasi kemasan untuk level ini.")
	ErrSerialCapacityExceeded = apperror.New(http.StatusConflict, "capacity_exceeded",
		"Kapasitas kemasan terlampaui", "Jumlah anak melebihi kapasitas kemasan induk.")
	ErrSerialMixedBatch = apperror.New(http.StatusUnprocessableEntity, "mixed_batch_not_allowed",
		"Batch tidak boleh dicampur", "Spesifikasi kemasan produk ini tidak mengizinkan campuran batch.")
	ErrSerialAlreadyPacked = apperror.New(http.StatusConflict, "already_packed",
		"Serial sudah di kemasan lain", "Serial sudah punya induk; bongkar dulu sebelum dipindah.")
	ErrSerialCycleDetected = apperror.New(http.StatusUnprocessableEntity, "cycle_detected",
		"Siklus kemasan", "Serial tidak boleh menjadi induk dari dirinya sendiri atau leluhurnya.")
	ErrSerialStatusMismatch = apperror.New(http.StatusConflict, "status_mismatch",
		"Status serial tidak sesuai", "Serial harus berstatus sama dan di lokasi yang sama untuk dikemas atau dibongkar.")
	ErrSerialEmptyContainer = apperror.New(http.StatusUnprocessableEntity, "empty_container",
		"Kemasan kosong", "Kemasan tanpa unit tidak bisa dikirim.")
	ErrShipmentNotFound = apperror.New(http.StatusNotFound, "shipment_not_found",
		"Shipment tidak ditemukan", "Shipment tidak ada.")
	ErrShipmentNotShipped = apperror.New(http.StatusConflict, "shipment_not_shipped",
		"Shipment tidak dalam perjalanan", "Hanya shipment berstatus SHIPPED yang bisa diterima.")
	ErrSerialConcurrentChange = apperror.New(http.StatusConflict, "concurrent_change",
		"Data berubah", "Serial berubah oleh proses lain saat diproses. Coba lagi.")
	ErrSerialDuplicateCode = apperror.New(http.StatusUnprocessableEntity, "duplicate_code",
		"Kode duplikat", "Kode yang sama muncul lebih dari sekali dalam satu permintaan.")
	ErrSerialNotUnit = apperror.New(http.StatusUnprocessableEntity, "serial_not_unit",
		"Bukan serial unit", "Hanya serial level UNIT yang bisa dijual; bongkar karton/palet terlebih dulu.")
	ErrShipmentToPartyMismatch = apperror.New(http.StatusUnprocessableEntity, "to_party_mismatch",
		"Mitra tujuan tidak cocok", "Tujuan adalah lokasi sendiri; to_party_id harus pemilik lokasi itu.")
	ErrClaimPreviouslyRejected = apperror.New(http.StatusForbidden, "claim_previously_rejected",
		"Klaim pernah ditolak", "Klaim akun ini untuk produk ini pernah ditolak. Hubungi layanan pelanggan.")
	ErrClaimReasonRequired = apperror.Validation(apperror.FieldError{
		Field: "note", Code: "required", Message: "alasan wajib diisi untuk REVERSE"})

	ErrClaimInvalidCodeOrPIN = apperror.New(http.StatusUnprocessableEntity, "invalid_code_or_pin",
		"Kode atau PIN salah", "Periksa kembali kode produk dan PIN di dalam kemasan.")
	ErrClaimLocked = apperror.New(http.StatusLocked, "claim_locked",
		"Klaim dikunci", "Terlalu banyak percobaan PIN salah untuk produk ini. Hubungi layanan pelanggan.")
	ErrClaimAlreadyClaimed = apperror.New(http.StatusConflict, "already_claimed",
		"Sudah diklaim", "Poin untuk produk ini sudah diklaim.")
	ErrClaimNotAllowed = apperror.New(http.StatusConflict, "claim_not_allowed",
		"Klaim belum bisa diproses", "Produk ini belum tercatat terjual. Simpan struk dan hubungi layanan pelanggan.")
	ErrClaimConsumerInactive = apperror.New(http.StatusForbidden, "consumer_inactive",
		"Akun tidak aktif", "Akun konsumen diblokir atau sudah dihapus.")
	ErrClaimNotFound = apperror.New(http.StatusNotFound, "claim_not_found",
		"Klaim tidak ditemukan", "Klaim tidak ada.")
	ErrClaimAlreadyDecided = apperror.New(http.StatusConflict, "claim_already_decided",
		"Klaim sudah diputuskan", "Klaim ini sudah disetujui atau ditolak.")
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
				Message: fmt.Sprintf("duplikat dengan %s[%d]", field, first)})
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
