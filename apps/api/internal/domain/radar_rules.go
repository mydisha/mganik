package domain

import (
	"encoding/json"
	"math"
	"time"
)

// Kode aturan radar dan risiko klaim (isi anomaly_rules). Ambang dibaca dari
// kolom params, bukan dari konstanta di kode.
const (
	RuleUnknownCode      = "UNKNOWN_CODE"
	RuleNonSellableScan  = "NON_SELLABLE_SCAN"
	RuleImpossibleTravel = "IMPOSSIBLE_TRAVEL"
	RuleScanVelocity     = "SCAN_VELOCITY"
	RuleOutOfTerritory   = "OUT_OF_TERRITORY"
	RuleBatchRecalled    = "BATCH_RECALLED"

	RuleClaimNearStore       = "CLAIM_NEAR_STORE"
	RuleClaimAccountVelocity = "CLAIM_ACCOUNT_VELOCITY"
	RuleClaimCartonSpread    = "CLAIM_CARTON_SPREAD"
	RuleClaimSharedDevice    = "CLAIM_SHARED_DEVICE"
	RuleClaimPINBruteForce   = "CLAIM_PIN_BRUTE_FORCE"
)

// Hasil verifikasi yang dilihat konsumen (scan_events.result). Tidak pernah
// membocorkan status internal serial.
const (
	ResultGenuineFirst  = "GENUINE_FIRST"
	ResultGenuineRepeat = "GENUINE_REPEAT"
	ResultSuspicious    = "SUSPICIOUS"
	ResultNotFound      = "NOT_FOUND"
	ResultRecalled      = "RECALLED"
)

// Efek aturan ke konsumen (anomaly_rules.consumer_effect).
const (
	EffectNone       = "NONE"
	EffectNotFound   = "NOT_FOUND"
	EffectSuspicious = "SUSPICIOUS"
	EffectRecalled   = "RECALLED"
)

// effectRank: makin besar makin menang saat beberapa aturan kena sekaligus.
// RECALLED menang atas SUSPICIOUS karena konsumen wajib menerima instruksi
// keselamatan produk.
var effectRank = map[string]int{EffectNone: 0, EffectSuspicious: 1, EffectRecalled: 2, EffectNotFound: 3}

// StrongestEffect mengembalikan efek konsumen paling kuat dari daftar efek.
func StrongestEffect(effects ...string) string {
	best := EffectNone
	for _, e := range effects {
		if effectRank[e] > effectRank[best] {
			best = e
		}
	}
	return best
}

// ---------------------------------------------------------------- parameter

// ParseRuleParams meng-decode params jsonb ke dst. Field yang tidak ada tetap
// bernilai default yang sudah diisi pemanggil di dst.
func ParseRuleParams(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// NonSellableParams: params NON_SELLABLE_SCAN.
type NonSellableParams struct {
	Statuses                     []string `json:"statuses"`
	IncludeUnshippedCentralStock bool     `json:"include_unshipped_central_stock"`
}

// ImpossibleTravelParams: params IMPOSSIBLE_TRAVEL.
type ImpossibleTravelParams struct {
	MaxSpeedKmh      float64 `json:"max_speed_kmh"`
	MinDistanceKmGPS float64 `json:"min_distance_km_gps"`
	MinDistanceKmIP  float64 `json:"min_distance_km_ip"`
}

// ScanVelocityParams: params SCAN_VELOCITY. Aturan kena hanya kalau jumlah
// fingerprint cookie DAN jumlah prefix IP berbeda sama-sama melewati ambang;
// MaxIPPrefixes 0 = sama dengan MaxFingerprints.
type ScanVelocityParams struct {
	MaxFingerprints int     `json:"max_fingerprints"`
	MaxIPPrefixes   int     `json:"max_ip_prefixes"`
	WindowHours     float64 `json:"window_hours"`
}

// ClaimRiskParams menampung params semua aturan CLAIM_*; tiap aturan hanya
// memakai field yang relevan.
type ClaimRiskParams struct {
	RiskWeight         int     `json:"risk_weight"`
	RadiusM            float64 `json:"radius_m"`
	DuringOpeningHours bool    `json:"during_opening_hours"`
	MaxClaims          int     `json:"max_claims"`
	MaxAccounts        int     `json:"max_accounts"`
	WindowHours        float64 `json:"window_hours"`
	MaxFailedAttempts  int     `json:"max_failed_attempts"`
}

// ---------------------------------------------------------------- impossible travel

// ScanPoint adalah lokasi satu scan beserta sumbernya.
type ScanPoint struct {
	Point  GeoPoint
	Source string // GPS atau IP
	At     time.Time
}

// TravelCheck adalah hasil evaluasi impossible travel.
type TravelCheck struct {
	Hit           bool
	DistanceKm    float64
	SpeedKmh      float64
	MinDistanceKm float64
}

// ImpossibleTravel membandingkan scan sekarang dengan scan sebelumnya. Jarak
// minimum lebih longgar kalau salah satu titik berasal dari IP (perkiraan
// kasar), supaya satu sinyal lemah tidak memicu anomali (ancaman 14).
func ImpossibleTravel(prev, cur ScanPoint, p ImpossibleTravelParams) TravelCheck {
	minDist := p.MinDistanceKmGPS
	if prev.Source != GeoGPS || cur.Source != GeoGPS {
		minDist = p.MinDistanceKmIP
	}
	dist := HaversineKm(prev.Point, cur.Point)
	// Selisih negatif (jam tidak sinkron / urutan tercatat terbalik) memakai
	// nilai absolut, bukan dianggap kecepatan tak hingga.
	hours := math.Abs(cur.At.Sub(prev.At).Hours())
	speed := math.Inf(1)
	if hours > 0 {
		speed = dist / hours
	}
	return TravelCheck{
		Hit:           dist >= minDist && speed > p.MaxSpeedKmh,
		DistanceKm:    dist,
		SpeedKmh:      speed,
		MinDistanceKm: minDist,
	}
}

// ---------------------------------------------------------------- skor risiko

// ClampRisk membatasi skor risiko ke 0–100. Bobot aturan boleh berjumlah di
// atas 100; kolom loyalty_claims.risk_score punya CHECK 0–100.
func ClampRisk(score int) int {
	return max(0, min(100, score))
}
