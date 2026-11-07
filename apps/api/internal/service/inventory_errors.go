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
		"Stok tidak cukup", "Stok tersedia di lokasi ini lebih sedikit dari jumlah yang diminta.")
	ErrBalanceNotFound = apperror.New(http.StatusNotFound, "balance_not_found",
		"Saldo stok tidak ditemukan", "Belum ada saldo stok untuk produk ini di lokasi tersebut.")
	ErrMovementTypeUnknown = apperror.New(http.StatusUnprocessableEntity, "movement_type_unknown",
		"Jenis mutasi tidak dikenal", "Jenis mutasi stok ini tidak terdaftar.")
	ErrInvalidMovement = apperror.New(http.StatusUnprocessableEntity, "invalid_movement",
		"Mutasi stok tidak valid", "Jumlah, arah, atau referensi mutasi stok tidak valid.")
	ErrStockLocationNotFound = apperror.New(http.StatusUnprocessableEntity, "location_not_found",
		"Lokasi tidak ditemukan", "Lokasi ini tidak ada atau sudah diarsipkan.")
	ErrStockProductNotFound = apperror.New(http.StatusUnprocessableEntity, "product_not_found",
		"Produk tidak ditemukan", "Produk ini tidak ada atau sudah diarsipkan.")

	ErrIdempotencyKeyRequired = apperror.New(http.StatusBadRequest, "idempotency_key_required",
		"Idempotency-Key wajib", "Header Idempotency-Key wajib diisi (1-255 karakter ASCII) untuk permintaan ini.")
	ErrIdempotencyKeyReused = apperror.New(http.StatusUnprocessableEntity, "idempotency_key_reused",
		"Permintaan bentrok", "Idempotency-Key ini sudah dipakai untuk permintaan dengan isi berbeda. Cek dulu apakah permintaan sebelumnya sudah tersimpan sebelum mengirim lagi.")
	ErrIdempotencyInProgress = apperror.New(http.StatusConflict, "idempotency_in_progress",
		"Masih diproses", "Permintaan yang sama masih diproses. Tunggu beberapa detik, lalu coba lagi.")

	ErrOrderNotFound = apperror.New(http.StatusNotFound, "order_not_found",
		"Order tidak ditemukan", "Order yang diminta tidak ada.")
	ErrOrderInvalidTransition = apperror.New(http.StatusConflict, "invalid_transition",
		"Status order tidak sesuai", "Aksi ini tidak berlaku untuk status order saat ini. Muat ulang untuk melihat status terbaru.")
	ErrOutOfStock = apperror.New(http.StatusConflict, "out_of_stock",
		"Stok tidak cukup", "Tidak ada satu lokasi pun yang punya stok untuk seluruh isi order ini. Kurangi jumlah pada baris yang ditandai, atau pindahkan baris itu ke order terpisah.")
	ErrOrderPriceNotFound = apperror.New(http.StatusUnprocessableEntity, "price_not_found",
		"Harga belum ada", "Produk ini belum punya harga yang berlaku di channel tersebut.")
	ErrOrderChannelInvalid = apperror.New(http.StatusUnprocessableEntity, "channel_invalid",
		"Channel tidak valid", "Channel ini tidak dikenal atau tidak bisa dipakai untuk aksi ini.")
	ErrOrderNotAllocated = apperror.New(http.StatusConflict, "order_not_allocated",
		"Stok order belum dipesan", "Sebagian isi order belum punya reservasi stok aktif, jadi order belum bisa diproses.")
	ErrOrderSerialInvalid = apperror.New(http.StatusUnprocessableEntity, "serial_invalid",
		"Serial tidak valid", "Periksa daftar serial: setiap baris harus milik order ini, dan setiap serial harus terdaftar, berlevel unit, sesuai produk barisnya, dan tidak dipakai dua kali. Jumlah serial per baris tidak boleh melebihi qty baris.")
	ErrOrderSerialSold = apperror.New(http.StatusConflict, "serial_already_sold",
		"Serial sudah terjual", "Salah satu serial sudah tercatat terjual di order lain. Pindai serial yang lain.")

	ErrPaymentAmountMismatch = apperror.New(http.StatusUnprocessableEntity, "amount_mismatch",
		"Nominal tidak cocok", "Nominal atau mata uang pembayaran tidak sama dengan total order.")
)
