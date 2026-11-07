package config

import (
	"fmt"
	"time"
)

// POSConfig mengatur POS offline-first: batas offline, heartbeat, dan batas
// batch sync (PLANNING §6.4, §7.4). Nilai nol = default (WithDefaults), supaya
// Config buatan test tetap aman dipakai.
type POSConfig struct {
	// OfflineLimit: transaksi yang terjadi lebih lama dari ini sebelum
	// diterima server tetap dicatat, tetapi diberi flag OFFLINE_TOO_LONG
	// (POS_OFFLINE_LIMIT, default 24h).
	OfflineLimit time.Duration
	// HeartbeatInterval: jeda heartbeat yang disarankan ke device
	// (POS_HEARTBEAT_INTERVAL, default 30s).
	HeartbeatInterval time.Duration
	// HeartbeatTimeout: lokasi tanpa heartbeat selama ini ditandai OFFLINE
	// oleh worker, sehingga ATP online toko itu 0 (POS_HEARTBEAT_TIMEOUT,
	// default 2m). Wajib lebih besar dari HeartbeatInterval.
	HeartbeatTimeout time.Duration
	// ClockSkewTolerance: selisih jam device vs server di atas ini diberi
	// flag CLOCK_SKEW (POS_CLOCK_SKEW_TOLERANCE, default 5m).
	ClockSkewTolerance time.Duration
	// SyncMaxBatch: maksimum transaksi per POST /pos/sync
	// (POS_SYNC_MAX_BATCH, default 500).
	SyncMaxBatch int
	// SyncMaxBodyBytes: batas body POST /pos/sync, menggantikan
	// HTTP_MAX_BODY_BYTES untuk route itu (POS_SYNC_MAX_BODY_BYTES, default 5 MiB).
	SyncMaxBodyBytes int64
	// HighDiscountPercent: diskon satu line di atas persentase ini dari
	// qty × harga diberi flag HIGH_DISCOUNT (POS_HIGH_DISCOUNT_PERCENT, default 50).
	HighDiscountPercent int
	// OfflineValueLimit: total transaksi (rupiah) di atas ini diberi flag
	// OFFLINE_VALUE_LIMIT + anomali; tetap diterima karena fakta
	// (POS_OFFLINE_VALUE_LIMIT, default 10.000.000).
	OfflineValueLimit int64
	// MaxSerialsPerTxn: batas serial per transaksi (selain <= Σ qty)
	// (POS_MAX_SERIALS_PER_TXN, default 2000).
	MaxSerialsPerTxn int
}

const (
	defaultPOSOfflineLimit       = 24 * time.Hour
	defaultPOSHeartbeatInterval  = 30 * time.Second
	defaultPOSHeartbeatTimeout   = 2 * time.Minute
	defaultPOSClockSkewTolerance = 5 * time.Minute
	defaultPOSSyncMaxBatch       = 500
	defaultPOSSyncMaxBodyBytes   = 5 << 20
	defaultPOSHighDiscountPct    = 50
	defaultPOSOfflineValueLimit  = 10_000_000
	defaultPOSMaxSerialsPerTxn   = 2000
	// maxPOSSyncBatch membatasi konfigurasi supaya satu request tidak bisa
	// menahan koneksi DB terlalu lama.
	maxPOSSyncBatch = 2000
)

// WithDefaults mengisi nilai nol dengan default.
func (c POSConfig) WithDefaults() POSConfig {
	if c.OfflineLimit <= 0 {
		c.OfflineLimit = defaultPOSOfflineLimit
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = defaultPOSHeartbeatInterval
	}
	if c.HeartbeatTimeout <= 0 {
		c.HeartbeatTimeout = defaultPOSHeartbeatTimeout
	}
	if c.ClockSkewTolerance <= 0 {
		c.ClockSkewTolerance = defaultPOSClockSkewTolerance
	}
	if c.SyncMaxBatch <= 0 {
		c.SyncMaxBatch = defaultPOSSyncMaxBatch
	}
	if c.SyncMaxBodyBytes <= 0 {
		c.SyncMaxBodyBytes = defaultPOSSyncMaxBodyBytes
	}
	if c.HighDiscountPercent <= 0 {
		c.HighDiscountPercent = defaultPOSHighDiscountPct
	}
	if c.OfflineValueLimit <= 0 {
		c.OfflineValueLimit = defaultPOSOfflineValueLimit
	}
	if c.MaxSerialsPerTxn <= 0 {
		c.MaxSerialsPerTxn = defaultPOSMaxSerialsPerTxn
	}
	return c
}

// pos membaca POS_* dan mencatat nilai yang tidak konsisten ke r.errs.
func (r reader) pos() POSConfig {
	c := POSConfig{
		OfflineLimit:       r.duration("POS_OFFLINE_LIMIT", defaultPOSOfflineLimit),
		HeartbeatInterval:  r.duration("POS_HEARTBEAT_INTERVAL", defaultPOSHeartbeatInterval),
		HeartbeatTimeout:   r.duration("POS_HEARTBEAT_TIMEOUT", defaultPOSHeartbeatTimeout),
		ClockSkewTolerance: r.duration("POS_CLOCK_SKEW_TOLERANCE", defaultPOSClockSkewTolerance),
		SyncMaxBatch:       r.positiveInt("POS_SYNC_MAX_BATCH", defaultPOSSyncMaxBatch),
		SyncMaxBodyBytes:   int64(r.positiveInt("POS_SYNC_MAX_BODY_BYTES", defaultPOSSyncMaxBodyBytes)),

		HighDiscountPercent: r.positiveInt("POS_HIGH_DISCOUNT_PERCENT", defaultPOSHighDiscountPct),
		OfflineValueLimit:   int64(r.positiveInt("POS_OFFLINE_VALUE_LIMIT", defaultPOSOfflineValueLimit)),
		MaxSerialsPerTxn:    r.positiveInt("POS_MAX_SERIALS_PER_TXN", defaultPOSMaxSerialsPerTxn),
	}
	if c.HighDiscountPercent > 100 {
		*r.errs = append(*r.errs, fmt.Errorf("POS_HIGH_DISCOUNT_PERCENT harus 1-100 (sekarang %d)", c.HighDiscountPercent))
	}
	if c.HeartbeatTimeout <= c.HeartbeatInterval {
		*r.errs = append(*r.errs, fmt.Errorf("POS_HEARTBEAT_TIMEOUT (%s) harus lebih besar dari POS_HEARTBEAT_INTERVAL (%s)",
			c.HeartbeatTimeout, c.HeartbeatInterval))
	}
	if c.SyncMaxBatch > maxPOSSyncBatch {
		*r.errs = append(*r.errs, fmt.Errorf("POS_SYNC_MAX_BATCH maksimal %d (sekarang %d)", maxPOSSyncBatch, c.SyncMaxBatch))
	}
	return c
}
