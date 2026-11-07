package service

import (
	"context"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/clock"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// Konteks scan (scan_events.context).
const (
	ScanContextConsumerVerify = "CONSUMER_VERIFY"
	ScanContextLoyaltyClaim   = "LOYALTY_CLAIM"
)

// regionMaxKm: titik scan dipetakan ke centroid kota/kabupaten terdekat
// selama jaraknya ≤ 50 km; di luar itu wilayah dianggap tidak diketahui.
const regionMaxKm = 50

// IPGeoResolver memperkirakan koordinat kasar dari IP klien. Prototype tidak
// memanggil layanan eksternal: implementasi default tidak tahu apa-apa.
type IPGeoResolver interface {
	Locate(ctx context.Context, ip string) (domain.GeoPoint, bool)
}

// UnknownIPGeo selalu menjawab "tidak diketahui" (geo_source NONE).
type UnknownIPGeo struct{}

func (UnknownIPGeo) Locate(context.Context, string) (domain.GeoPoint, bool) {
	return domain.GeoPoint{}, false
}

// ScanIdentity menghitung pengenal anonim scan: fingerprint perangkat dari id
// cookie anonim acak, dan hash prefix IP (/24 IPv4, /48 IPv6). Keduanya HMAC
// dengan sub-kunci turunan CLAIM_PEPPER (pemisah domain), jadi nilai mentah
// tidak pernah disimpan dan tidak bisa dicocokkan tanpa secret.
type ScanIdentity struct {
	fp *domain.KeyedHasher
	ip *domain.KeyedHasher
}

// NewScanIdentity menyiapkan hasher (ipad/opad dihitung sekali).
func NewScanIdentity(pepper []byte) *ScanIdentity {
	return &ScanIdentity{
		fp: domain.NewKeyedHasher(domain.DeriveKey(pepper, "mganik/scan-fingerprint/v1")),
		ip: domain.NewKeyedHasher(domain.DeriveKey(pepper, "mganik/scan-ip-prefix/v1")),
	}
}

// Fingerprint mengembalikan HMAC id cookie anonim (nil kalau kosong).
func (s *ScanIdentity) Fingerprint(anonID string) []byte {
	if anonID == "" {
		return nil
	}
	sum := s.fp.SumString(anonID)
	return sum[:]
}

// IPHash mengembalikan HMAC prefix IP (nil kalau IP tidak valid).
func (s *ScanIdentity) IPHash(ip string) []byte {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return nil
	}
	addr = addr.Unmap()
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return nil
	}
	sum := s.ip.SumString(prefix.String())
	return sum[:]
}

// scanGeo adalah lokasi scan beserta sumbernya.
type scanGeo struct {
	Point     domain.GeoPoint
	Source    string
	AccuracyM *int
}

func (g scanGeo) known() bool { return g.Source != domain.GeoNone }

// VerifyInput adalah satu permintaan verifikasi publik.
type VerifyInput struct {
	RawCode   string
	GPS       *domain.GeoPoint
	AccuracyM *int
	ClientIP  string
	AnonID    string
	// IPGeoOverride: koordinat "IP" untuk uji/demo. Controller hanya mengisinya
	// di luar production (header X-Test-IP-Geo).
	IPGeoOverride *domain.GeoPoint
}

// VerifyResult adalah jawaban untuk konsumen. Tidak memuat status internal.
type VerifyResult struct {
	Result            string
	ProductName       *string
	LotNo             *string
	ExpDate           *time.Time
	FirstVerifiedAt   *time.Time
	FirstVerifiedCity *string
	// RuleHits hanya untuk log/test internal; tidak dikirim ke konsumen.
	RuleHits []string
}

// VerifyService menjalankan verifikasi publik dan radar anti-counterfeit.
type VerifyService struct {
	db    *gorm.DB
	repo  *repository.VerifyRepository
	clock clock.Clock
	ids   *ScanIdentity
	geo   IPGeoResolver
}

// NewVerifyService membuat service. geo nil = UnknownIPGeo.
func NewVerifyService(db *gorm.DB, clk clock.Clock, pepper []byte, geo IPGeoResolver) *VerifyService {
	if clk == nil {
		clk = clock.Real{}
	}
	if geo == nil {
		geo = UnknownIPGeo{}
	}
	return &VerifyService{db: db, repo: repository.NewVerifyRepository(), clock: clk, ids: NewScanIdentity(pepper), geo: geo}
}

func (s *VerifyService) locate(ctx context.Context, gps *domain.GeoPoint, accuracy *int, ip string, override *domain.GeoPoint) scanGeo {
	if gps != nil && gps.Valid() {
		return scanGeo{Point: *gps, Source: domain.GeoGPS, AccuracyM: accuracy}
	}
	if override != nil && override.Valid() {
		return scanGeo{Point: *override, Source: domain.GeoIP}
	}
	if p, ok := s.geo.Locate(ctx, ip); ok && p.Valid() {
		return scanGeo{Point: p, Source: domain.GeoIP}
	}
	return scanGeo{Source: domain.GeoNone}
}

// truncateRunes memotong s ke maksimal n karakter (bukan byte).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// Verify memvalidasi keaslian kode untuk konsumen.
//
// Kode berformat salah langsung NOT_FOUND tanpa lookup ke serial_units, tetapi
// tetap direkam (raw dipotong 64 karakter) untuk hotspot. Selain itu satu
// transaksi: serial dikunci, aturan radar §7.6 dievaluasi dengan params dari
// anomaly_rules, scan_events direkam apa adanya, anomali di-upsert lewat
// dedupe_key, dan verify_count/first_verified_at diperbarui atomik.
// OUT_OF_TERRITORY hanya membuat anomali internal; konsumen tetap GENUINE.
func (s *VerifyService) Verify(ctx context.Context, in VerifyInput) (VerifyResult, error) {
	code, ok := domain.NormalizeSerialCode(in.RawCode)
	now := s.clock.Now()
	geo := s.locate(ctx, in.GPS, in.AccuracyM, in.ClientIP, in.IPGeoOverride)
	fp := s.ids.Fingerprint(in.AnonID)
	ipHash := s.ids.IPHash(in.ClientIP)
	if !ok {
		return s.recordMalformed(ctx, in.RawCode, geo, fp, ipHash, now)
	}

	var res VerifyResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rules, err := s.repo.Rules(tx, "SCAN")
		if err != nil {
			return err
		}
		var region *repository.RegionHit
		if geo.known() {
			if region, err = s.repo.ResolveRegion(tx, geo.Point, regionMaxKm); err != nil {
				return err
			}
		}
		scan, err := newScanEvent(in.RawCode, ScanContextConsumerVerify, geo, region, fp, ipHash, now)
		if err != nil {
			return err
		}

		unit, found, err := s.repo.FindForVerify(tx, code)
		if err != nil {
			return err
		}
		if !found {
			res.Result = domain.ResultNotFound
			scan.Result = domain.ResultNotFound
			if rule, ok := rules[domain.RuleUnknownCode]; ok {
				scan.RuleHits = model.StringArray{rule.Code}
				res.RuleHits = scan.RuleHits
				if err := s.repo.UpsertAnomaly(tx, unknownCodeAnomaly(rule, code, geo, region, now)); err != nil {
					return err
				}
			}
			return s.repo.InsertScan(tx, scan)
		}

		hits, err := s.evaluate(tx, unit, scanContext{now: now, geo: geo, region: region, fp: fp, ipHash: ipHash}, rules)
		if err != nil {
			return err
		}
		effects := make([]string, 0, len(hits))
		for _, h := range hits {
			effects = append(effects, h.effect)
			scan.RuleHits = append(scan.RuleHits, h.rule.Code)
		}
		effect := domain.StrongestEffect(effects...)
		mark, err := s.repo.MarkVerified(tx, unit.ID, now, regionCodeOf(region), effect == domain.EffectNone)
		if err != nil {
			return err
		}

		res.RuleHits = scan.RuleHits
		res.FirstVerifiedAt, res.FirstVerifiedCity = mark.PrevFirstAt, mark.PrevRegionName
		switch effect {
		case domain.EffectNotFound:
			res.Result = domain.ResultNotFound
			res.FirstVerifiedAt, res.FirstVerifiedCity = nil, nil
		case domain.EffectRecalled:
			res.Result = domain.ResultRecalled
			res.ProductName, res.LotNo, res.ExpDate = unit.ProductName, unit.LotNo, unit.ExpDate
		case domain.EffectSuspicious:
			res.Result = domain.ResultSuspicious
		default:
			res.ProductName, res.LotNo, res.ExpDate = unit.ProductName, unit.LotNo, unit.ExpDate
			if mark.PrevFirstAt == nil {
				res.Result = domain.ResultGenuineFirst
				res.FirstVerifiedAt, res.FirstVerifiedCity = mark.FirstAtAfter, mark.RegionNameAfter
			} else {
				res.Result = domain.ResultGenuineRepeat
			}
		}

		scan.SerialUnitID = &unit.ID
		scan.Result = res.Result
		if err := s.repo.InsertScan(tx, scan); err != nil {
			return err
		}
		for _, h := range hits {
			if err := s.repo.UpsertAnomaly(tx, h.anomaly); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return VerifyResult{}, err
	}
	return res, nil
}

// malformedRawMax: raw_code kode berformat salah dipotong ke 64 karakter
// (cukup untuk melihat pola label palsu, tanpa menyimpan muatan besar).
const malformedRawMax = 64

// recordMalformed merekam scan kode berformat salah sebagai NOT_FOUND dan
// menaikkan hotspot UNKNOWN_CODE per wilayah/hari, TANPA lookup ke
// serial_units (format sudah pasti tidak ada di DB). Tujuannya agar label
// palsu yang formatnya pun salah tetap terlihat di peta hotspot.
func (s *VerifyService) recordMalformed(ctx context.Context, raw string, geo scanGeo, fp, ipHash []byte, now time.Time) (VerifyResult, error) {
	res := VerifyResult{Result: domain.ResultNotFound}
	raw = truncateRunes(strings.ReplaceAll(raw, "\x00", ""), malformedRawMax)
	if raw == "" {
		return res, nil // raw_code wajib 1-512 karakter; tidak ada yang bisa direkam
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rules, err := s.repo.Rules(tx, "SCAN")
		if err != nil {
			return err
		}
		var region *repository.RegionHit
		if geo.known() {
			if region, err = s.repo.ResolveRegion(tx, geo.Point, regionMaxKm); err != nil {
				return err
			}
		}
		scan, err := newScanEvent(raw, ScanContextConsumerVerify, geo, region, fp, ipHash, now)
		if err != nil {
			return err
		}
		scan.Result = domain.ResultNotFound
		if rule, ok := rules[domain.RuleUnknownCode]; ok {
			scan.RuleHits = model.StringArray{rule.Code}
			res.RuleHits = scan.RuleHits
			hit := unknownCodeAnomaly(rule, raw, geo, region, now)
			hit.Evidence["malformed"] = true
			if err := s.repo.UpsertAnomaly(tx, hit); err != nil {
				return err
			}
		}
		return s.repo.InsertScan(tx, scan)
	})
	if err != nil {
		return VerifyResult{}, err
	}
	return res, nil
}

func regionCodeOf(r *repository.RegionHit) *string {
	if r == nil {
		return nil
	}
	return &r.Code
}

// newScanEvent menyiapkan baris scan_events. raw_code disimpan apa adanya
// (dipotong ke 512 karakter sesuai CHECK).
func newScanEvent(raw, context string, geo scanGeo, region *repository.RegionHit, fp, ipHash []byte, now time.Time) (*model.ScanEvent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	scan := &model.ScanEvent{
		ID: id, RawCode: truncateRunes(strings.ReplaceAll(raw, "\x00", ""), 512), Context: context,
		ActorFingerprint: fp, IPHash: ipHash, GeoSource: geo.Source,
		RegionCode: regionCodeOf(region), RuleHits: model.StringArray{}, OccurredAt: now,
	}
	if geo.known() {
		lat, lng := geo.Point.Lat, geo.Point.Lng
		scan.Lat, scan.Lng, scan.AccuracyM = &lat, &lng, geo.AccuracyM
	}
	return scan, nil
}
