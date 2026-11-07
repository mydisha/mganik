package service

import (
	"net/http"

	"mganik/api/internal/apperror"
)

// Permission domain POS.
const (
	PermPosSync         = DevicePermission // "pos.sync"
	PermPosDeviceManage = "pos.device.manage"
	PermConflictResolve = "conflict.resolve"
)

// Status hasil per transaksi di respons POST /pos/sync. ACCEPTED,
// ACCEPTED_WITH_CONFLICT, dan REJECTED juga nilai pos_transactions.result.
// DUPLICATE = transaksi sudah pernah diproses (result & detail asli
// dikembalikan apa adanya). RETRY = gagal sementara, TIDAK disimpan; klien
// mempertahankannya di outbox dan mengirim ulang.
const (
	PosStatusAccepted             = "ACCEPTED"
	PosStatusAcceptedWithConflict = "ACCEPTED_WITH_CONFLICT"
	PosStatusRejected             = "REJECTED"
	PosStatusDuplicate            = "DUPLICATE"
	PosStatusRetry                = "RETRY"
)

// Alasan REJECTED (result_detail.reason) dan RETRY. Stabil, snake_case.
const (
	PosReasonInvalidPayload       = "invalid_payload"
	PosReasonInvalidSignature     = "invalid_signature"
	PosReasonCashierNotAllowed    = "cashier_not_allowed"
	PosReasonUnknownProduct       = "unknown_product"
	PosReasonValidationFailed     = "validation_failed"
	PosReasonInvalidCharacters    = "invalid_characters"
	PosReasonTotalMismatch        = "total_mismatch"
	PosReasonPaymentInsufficient  = "payment_insufficient"
	PosReasonIdempotencyKeyReused = "idempotency_key_reused"
	PosReasonDeviceSeqReused      = "device_seq_reused"
	PosReasonStockContended       = "stock_contended"
	PosReasonTemporaryFailure     = "temporary_failure"
)

// Flag transaksi yang diterima (result_detail.flags).
const (
	PosFlagPriceStale      = "PRICE_STALE"
	PosFlagOfflineTooLong  = "OFFLINE_TOO_LONG"
	PosFlagClockSkew       = "CLOCK_SKEW"
	PosFlagFutureTimestamp = "FUTURE_TIMESTAMP"
	// PRICE_MISMATCH: unit_price beda dari price list yang berlaku pada waktu
	// transaksi (atau produk tidak ada di list itu).
	PosFlagPriceMismatch = "PRICE_MISMATCH"
	// HIGH_DISCOUNT: diskon satu line > POS_HIGH_DISCOUNT_PERCENT.
	PosFlagHighDiscount = "HIGH_DISCOUNT"
	// OFFLINE_VALUE_LIMIT: total > POS_OFFLINE_VALUE_LIMIT (+ anomali).
	PosFlagOfflineValueLimit = "OFFLINE_VALUE_LIMIT"
	// ZERO_TOTAL: total transaksi 0.
	PosFlagZeroTotal = "ZERO_TOTAL"
	// CASHIER_INACTIVE: kasir punya role di lokasi tetapi user nonaktif (+ anomali).
	PosFlagCashierInactive = "CASHIER_INACTIVE"
	// SERIAL_INVALID_FORMAT: kode yang dipindai bukan format serial.
	PosFlagSerialInvalidFormat = "SERIAL_INVALID_FORMAT"
)

// Jenis konflik di result_detail.conflicts. Tiga yang pertama adalah
// allocation_conflicts.kind; SERIAL_UNREGISTERED dan PAYMENT_REF_DUPLICATE
// dicatat sebagai anomali (tanpa baris allocation_conflicts).
const (
	PosConflictPreempt          = "OFFLINE_SALE_PREEMPT"
	PosConflictDiscrepancy      = "STOCK_DISCREPANCY"
	PosConflictSerial           = "SERIAL_DOUBLE_SALE"
	PosConflictSerialUnknown    = "SERIAL_UNREGISTERED"
	PosConflictPaymentDuplicate = "PAYMENT_REF_DUPLICATE"
)

var (
	ErrPosDeviceNotFound = apperror.New(http.StatusNotFound, "device_not_found",
		"Perangkat tidak ditemukan", "Perangkat kasir yang diminta tidak ada.")
	ErrPosLocationNotStore = apperror.New(http.StatusUnprocessableEntity, "location_not_store",
		"Lokasi bukan toko", "Perangkat kasir hanya bisa dipasang di toko milik sendiri yang aktif.")
	ErrPosBatchTooLarge = apperror.New(http.StatusUnprocessableEntity, "batch_too_large",
		"Kiriman terlalu besar", "Jumlah transaksi dalam satu sinkronisasi melebihi batas. Kirim dalam beberapa bagian.")
	ErrPosSigningKeyMissing = apperror.New(http.StatusConflict, "signing_key_missing",
		"Kunci perangkat belum ada", "Perangkat kasir ini belum punya kunci tanda tangan. Daftarkan ulang perangkatnya.")
	ErrPosConflictNotFound = apperror.New(http.StatusNotFound, "conflict_not_found",
		"Konflik tidak ditemukan", "Konflik alokasi ini tidak ada atau di luar akses akun ini.")
	ErrPosConflictNotOpen = apperror.New(http.StatusConflict, "invalid_transition",
		"Konflik sudah ditutup", "Konflik ini sudah diselesaikan, otomatis atau oleh staf lain. Muat ulang untuk melihat penyelesaiannya.")
	ErrPosResolutionNotApplicable = apperror.New(http.StatusConflict, "resolution_not_applicable",
		"Penyelesaian tidak sesuai", "ORDER_CANCELLED hanya bisa dipilih kalau konflik ini punya order terkait dan order itu sudah dibatalkan atau kedaluwarsa. Pilih penyelesaian lain.")
)
