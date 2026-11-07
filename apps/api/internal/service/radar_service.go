package service

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"time"

	"gorm.io/gorm"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// scanContext adalah atribut scan yang dibutuhkan aturan radar.
type scanContext struct {
	now    time.Time
	geo    scanGeo
	region *repository.RegionHit
	fp     []byte
	ipHash []byte
}

// radarHit adalah satu aturan yang kena beserta anomali yang di-upsert.
// effect = efek ke konsumen; default anomaly_rules.consumer_effect, bisa
// diturunkan ke NONE kalau sinyalnya lemah (lihat IMPOSSIBLE_TRAVEL).
type radarHit struct {
	rule    model.AnomalyRule
	effect  string
	anomaly repository.RadarAnomalyHit
}

func newRadarHit(rule model.AnomalyRule, subjectType, dedupe string, unit repository.VerifyUnit, sc scanContext, evidence map[string]any) radarHit {
	subject := unit.ID
	return radarHit{rule: rule, effect: rule.ConsumerEffect, anomaly: repository.RadarAnomalyHit{
		RuleCode: rule.Code, Severity: rule.Severity, SubjectType: subjectType, SubjectID: &subject,
		PartyID: unit.CustodianPartyID, RegionCode: regionCodeOf(sc.region),
		DedupeKey: dedupe, Evidence: evidence, At: sc.now,
	}}
}

// evaluate menjalankan aturan radar §7.6 (kecuali UNKNOWN_CODE) untuk serial
// yang terdaftar. Hanya aturan enabled yang dievaluasi; ambang dibaca dari
// params tiap aturan. Query yang dipakai memakai index (serial_unit_id,
// occurred_at) sehingga murah dijalankan sinkron di request.
func (s *VerifyService) evaluate(tx *gorm.DB, unit repository.VerifyUnit, sc scanContext, rules map[string]model.AnomalyRule) ([]radarHit, error) {
	var hits []radarHit
	base := map[string]any{"serial_code": unit.Code, "geo_source": sc.geo.Source}
	if sc.region != nil {
		base["region_code"], base["region_name"] = sc.region.Code, sc.region.Name
	}
	evidence := func(extra map[string]any) map[string]any {
		out := make(map[string]any, len(base)+len(extra))
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// BATCH_RECALLED: batch ditarik; konsumen menerima instruksi recall.
	if rule, ok := rules[domain.RuleBatchRecalled]; ok && unit.BatchStatus != nil && *unit.BatchStatus == "RECALLED" {
		h := newRadarHit(rule, "BATCH", "BATCH_RECALLED:"+unit.BatchID.String(), unit, sc,
			evidence(map[string]any{"lot_no": unit.LotNo}))
		h.anomaly.SubjectID = unit.BatchID
		hits = append(hits, h)
	}

	// NON_SELLABLE_SCAN: unit belum/tidak beredar dipindai konsumen (label
	// bocor dari vendor/pabrik), atau stok gudang pusat yang belum pernah dikirim.
	if rule, ok := rules[domain.RuleNonSellableScan]; ok {
		p := domain.NonSellableParams{
			Statuses:                     []string{domain.SerialGenerated, domain.SerialCommissioned, domain.SerialDecommissioned},
			IncludeUnshippedCentralStock: true,
		}
		if err := domain.ParseRuleParams(rule.Params, &p); err != nil {
			return nil, fmt.Errorf("params %s: %w", rule.Code, err)
		}
		hit := slices.Contains(p.Statuses, unit.Status)
		reason := "status"
		if !hit && p.IncludeUnshippedCentralStock && unit.Status == domain.SerialInStock &&
			unit.LocationTypeCode != nil && *unit.LocationTypeCode == "CENTRAL_WH" {
			shipped, err := s.repo.EverShipped(tx, unit.ID)
			if err != nil {
				return nil, err
			}
			hit, reason = !shipped, "unshipped_central_stock"
		}
		if hit {
			hits = append(hits, newRadarHit(rule, "SERIAL_UNIT", "NON_SELLABLE_SCAN:"+unit.ID.String(), unit, sc,
				evidence(map[string]any{"status": unit.Status, "reason": reason, "code_batch_id": unit.CodeBatchID})))
		}
	}

	// IMPOSSIBLE_TRAVEL: jarak ke scan sebelumnya ÷ selisih waktu melebihi batas.
	//
	// Trade-off anti-grief: koordinat GPS dikirim klien dan mudah dipalsukan.
	// Kalau scan sebelumnya berasal dari perangkat (fingerprint) yang SAMA,
	// "teleportasi" itu lebih mungkin GPS palsu/salah dari satu orang daripada
	// QR kloning, jadi hanya dicatat sebagai anomali internal (konsumen tetap
	// GENUINE). Hasil konsumen menjadi SUSPICIOUS hanya kalau scan sebelumnya
	// dari fingerprint BERBEDA (dua perangkat di dua tempat = pola kloning).
	// Konsekuensinya: pemalsu yang memindai kloning dari perangkat yang sama
	// dengan scan terakhir tidak diberi tahu SUSPICIOUS; anomalinya tetap masuk
	// antrean review.
	if rule, ok := rules[domain.RuleImpossibleTravel]; ok && sc.geo.known() {
		p := domain.ImpossibleTravelParams{MaxSpeedKmh: 900, MinDistanceKmGPS: 100, MinDistanceKmIP: 500}
		if err := domain.ParseRuleParams(rule.Params, &p); err != nil {
			return nil, fmt.Errorf("params %s: %w", rule.Code, err)
		}
		prev, err := s.repo.LastGeoScan(tx, unit.ID)
		if err != nil {
			return nil, err
		}
		if prev != nil && prev.Lat != nil && prev.Lng != nil {
			check := domain.ImpossibleTravel(
				domain.ScanPoint{Point: domain.GeoPoint{Lat: *prev.Lat, Lng: *prev.Lng}, Source: prev.GeoSource, At: prev.OccurredAt},
				domain.ScanPoint{Point: sc.geo.Point, Source: sc.geo.Source, At: sc.now}, p)
			if check.Hit {
				speed := check.SpeedKmh
				if math.IsInf(speed, 1) {
					speed = -1 // JSON tidak bisa memuat Inf; -1 = selisih waktu nol
				}
				otherDevice := len(prev.ActorFingerprint) > 0 && len(sc.fp) > 0 && !bytes.Equal(prev.ActorFingerprint, sc.fp)
				h := newRadarHit(rule, "SERIAL_UNIT", "IMPOSSIBLE_TRAVEL:"+unit.ID.String(), unit, sc,
					evidence(map[string]any{
						"distance_km": math.Round(check.DistanceKm*10) / 10, "speed_kmh": math.Round(speed),
						"min_distance_km": check.MinDistanceKm, "prev_scan_id": prev.ID,
						"prev_geo_source": prev.GeoSource, "prev_region_code": prev.RegionCode,
						"minutes_since_prev": math.Round(sc.now.Sub(prev.OccurredAt).Minutes()),
						"different_device":   otherDevice,
					}))
				if !otherDevice {
					h.effect = domain.EffectNone
				}
				hits = append(hits, h)
			}
		}
	}

	// SCAN_VELOCITY: terlalu banyak pemindai berbeda untuk unit yang sama.
	//
	// Trade-off anti-grief: cookie anonim dikendalikan klien, jadi satu orang
	// bisa membuat "6 perangkat" dengan menghapus cookie dan menjadikan unit
	// asli SUSPICIOUS. Aturan baru kena kalau jumlah fingerprint DAN jumlah
	// prefix IP berbeda (/24 IPv4, /48 IPv6) sama-sama melewati ambang.
	// Konsekuensinya: kloning yang dipindai banyak orang dari satu jaringan
	// (mis. satu kantor/NAT operator) tidak terdeteksi oleh aturan ini.
	if rule, ok := rules[domain.RuleScanVelocity]; ok && sc.fp != nil {
		p := domain.ScanVelocityParams{MaxFingerprints: 5, WindowHours: 24}
		if err := domain.ParseRuleParams(rule.Params, &p); err != nil {
			return nil, fmt.Errorf("params %s: %w", rule.Code, err)
		}
		if p.MaxIPPrefixes <= 0 {
			p.MaxIPPrefixes = p.MaxFingerprints
		}
		since := sc.now.Add(-time.Duration(p.WindowHours * float64(time.Hour)))
		n, err := s.repo.DistinctFingerprints(tx, unit.ID, since, sc.fp)
		if err != nil {
			return nil, err
		}
		if n > p.MaxFingerprints {
			ips, err := s.repo.DistinctIPHashes(tx, unit.ID, since, sc.ipHash)
			if err != nil {
				return nil, err
			}
			if ips > p.MaxIPPrefixes {
				hits = append(hits, newRadarHit(rule, "SERIAL_UNIT", "SCAN_VELOCITY:"+unit.ID.String(), unit, sc,
					evidence(map[string]any{
						"fingerprints": n, "ip_prefixes": ips, "window_hours": p.WindowHours,
						"max_fingerprints": p.MaxFingerprints, "max_ip_prefixes": p.MaxIPPrefixes,
					})))
			}
		}
	}

	// OUT_OF_TERRITORY: wilayah scan di luar cakupan custodian. Mode cakupan
	// dibaca dari partner_territories.coverage_mode; party tanpa baris cakupan
	// yang berlaku dilewati (tidak diasumsikan nasional atau kosong).
	if rule, ok := rules[domain.RuleOutOfTerritory]; ok && sc.region != nil && unit.CustodianPartyID != nil {
		cov, err := s.repo.CoverageFor(tx, *unit.CustodianPartyID, sc.region.Code, sc.region.ParentCode, sc.now)
		if err != nil {
			return nil, err
		}
		if cov.Known && !cov.Covered {
			hits = append(hits, newRadarHit(rule, "SERIAL_UNIT",
				"OUT_OF_TERRITORY:"+unit.ID.String()+":"+sc.region.Code, unit, sc,
				evidence(map[string]any{"custodian_party_id": unit.CustodianPartyID, "status": unit.Status})))
		}
	}
	return hits, nil
}

// unknownCodeAnomaly: kode tidak terdaftar diagregasi per wilayah per hari
// (hotspot pemalsuan), bukan satu anomali per kode.
func unknownCodeAnomaly(rule model.AnomalyRule, code string, geo scanGeo, region *repository.RegionHit, now time.Time) repository.RadarAnomalyHit {
	regionKey := "NONE"
	if region != nil {
		regionKey = region.Code
	}
	return repository.RadarAnomalyHit{
		RuleCode: rule.Code, Severity: rule.Severity, SubjectType: "REGION",
		RegionCode: regionCodeOf(region),
		DedupeKey:  "UNKNOWN_CODE:" + regionKey + ":" + now.UTC().Format("2006-01-02"),
		Evidence:   map[string]any{"sample_code": code, "geo_source": geo.Source},
		At:         now,
	}
}
