package service

import (
	"net/http"

	"mganik/api/internal/apperror"
)

// Error domain stok, order, dan pembayaran. Kode stabil (snake_case Inggris)
// dipakai frontend; pesan berbahasa Indonesia. Nama diberi awalan domain
// supaya tidak bertabrakan dengan error domain lain di package service.
var (
	ErrInsufficientStock = apperror.New(http.StatusConflict, "insufficient_stock",
		"Stok tidak cukup", "Stok tersedia tidak mencukupi untuk jumlah yang diminta.")
	ErrBalanceNotFound = apperror.New(http.StatusNotFound, "balance_not_found",
		"Saldo stok tidak ditemukan", "Belum ada saldo stok untuk produk ini di lokasi tersebut.")
	ErrMovementTypeUnknown = apperror.New(http.StatusUnprocessableEntity, "movement_type_unknown",
		"Tipe mutasi tidak dikenal", "Tipe mutasi stok tidak terdaftar.")
	ErrInvalidMovement = apperror.New(http.StatusUnprocessableEntity, "invalid_movement",
		"Mutasi stok tidak valid", "Jumlah, arah, atau referensi mutasi stok tidak valid.")
	ErrStockLocationNotFound = apperror.New(http.StatusUnprocessableEntity, "location_not_found",
		"Lokasi tidak ditemukan", "Lokasi tidak ada atau sudah diarsipkan.")
	ErrStockProductNotFound = apperror.New(http.StatusUnprocessableEntity, "product_not_found",
		"Produk tidak ditemukan", "Produk tidak ada atau sudah diarsipkan.")

	ErrIdempotencyKeyRequired = apperror.New(http.StatusBadRequest, "idempotency_key_required",
		"Idempotency-Key wajib", "Header Idempotency-Key wajib diisi (1-255 karakter ASCII) untuk request ini.")
	ErrIdempotencyKeyReused = apperror.New(http.StatusUnprocessableEntity, "idempotency_key_reused",
		"Idempotency-Key dipakai ulang", "Idempotency-Key ini sudah dipakai untuk request dengan isi berbeda.")
	ErrIdempotencyInProgress = apperror.New(http.StatusConflict, "idempotency_in_progress",
		"Request masih diproses", "Request dengan Idempotency-Key yang sama masih diproses. Coba lagi sebentar.")

	ErrOrderNotFound = apperror.New(http.StatusNotFound, "order_not_found",
		"Order tidak ditemukan", "Order tidak ada.")
	ErrOrderInvalidTransition = apperror.New(http.StatusConflict, "invalid_transition",
		"Status order tidak bisa diubah", "Aksi ini tidak diizinkan untuk status order saat ini.")
	ErrOutOfStock = apperror.New(http.StatusConflict, "out_of_stock",
		"Stok habis", "Tidak ada lokasi yang bisa memenuhi seluruh isi order saat ini.")
	ErrOrderPriceNotFound = apperror.New(http.StatusUnprocessableEntity, "price_not_found",
		"Harga tidak ditemukan", "Produk belum punya harga yang berlaku di channel ini.")
	ErrOrderChannelInvalid = apperror.New(http.StatusUnprocessableEntity, "channel_invalid",
		"Channel tidak valid", "Channel tidak dikenal atau tidak boleh dipakai untuk aksi ini.")
	ErrOrderNotAllocated = apperror.New(http.StatusConflict, "order_not_allocated",
		"Order belum teralokasi", "Sebagian isi order tidak punya reservasi stok aktif.")
	ErrOrderSerialInvalid = apperror.New(http.StatusUnprocessableEntity, "serial_invalid",
		"Serial tidak valid", "Serial tidak ada, bukan level unit, melebihi qty, atau tidak cocok dengan produk line.")
	ErrOrderSerialSold = apperror.New(http.StatusConflict, "serial_already_sold",
		"Serial sudah terjual", "Salah satu serial sudah tercatat terjual di order lain.")

	ErrPaymentAmountMismatch = apperror.New(http.StatusUnprocessableEntity, "amount_mismatch",
		"Nominal tidak cocok", "Nominal atau mata uang pembayaran tidak sama dengan total order.")
)
