package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// defaultMaxPINAttempts dipakai kalau params CLAIM_PIN_BRUTE_FORCE kosong.
// Batas ini kontrol keamanan, jadi tetap berlaku walau aturannya dinonaktifkan.
const defaultMaxPINAttempts = 5

// Keputusan review klaim. APPROVE/REJECT untuk klaim PENDING; REVERSE
// (clawback, alasan wajib) untuk klaim yang sudah APPROVED.
const (
	ClaimDecisionApprove = "APPROVE"
	ClaimDecisionReject  = "REJECT"
	ClaimDecisionReverse = "REVERSE"
)

// claimReasonPriorRejected: alasan review paksa (bukan aturan anomaly_rules),
// unit pernah punya klaim yang ditolak/dibalik.
const claimReasonPriorRejected = "PRIOR_CLAIM_REJECTED"

// LoyaltyService menangani klaim poin dengan PIN (Kasus 2), review staff,
// pelepasan poin setelah masa tunggu, dan saldo.
type LoyaltyService struct {
	db      *gorm.DB
	repo    *repository.LoyaltyRepository
	verify  *repository.VerifyRepository
	serials *SerialService
	clock   clock.Clock
	pepper  []byte
	ids     *ScanIdentity
	cfg     config.SerializationConfig
}

// NewLoyaltyService membuat service. serials dipakai untuk transisi
// CONSUMER_ACTIVATED lewat state machine yang sama.
func NewLoyaltyService(db *gorm.DB, clk clock.Clock, pepper []byte, cfg config.SerializationConfig, serials *SerialService) *LoyaltyService {
	if clk == nil {
		clk = clock.Real{}
	}
	return &LoyaltyService{
		db: db, repo: repository.NewLoyaltyRepository(), verify: repository.NewVerifyRepository(),
		serials: serials, clock: clk, pepper: append([]byte(nil), pepper...),
		ids: NewScanIdentity(pepper), cfg: cfg.WithDefaults(),
	}
}

// ClaimInput adalah permintaan klaim poin konsumen.
type ClaimInput struct {
	Code      string
	PIN       string
	GPS       *domain.GeoPoint
	AccuracyM *int
	ClientIP  string
	AnonID    string
}

// RiskReason adalah satu aturan risiko yang kena.
type RiskReason struct {
	Rule   string `json:"rule"`
	Weight int    `json:"weight"`
}

// ClaimResult adalah klaim yang tercatat.
type ClaimResult struct {
	Claim       model.LoyaltyClaim
	RiskReasons []RiskReason
	// Replayed: klaim yang sama (akun sama, PIN benar) dikirim ulang; klaim
	// lama dikembalikan tanpa perubahan.
	Replayed bool
}

// Claim memproses klaim poin dengan PIN di dalam kemasan.
//
// Urutan di satu transaksi, dengan serial dikunci FOR UPDATE sehingga semua
// percobaan untuk unit yang sama diserialkan:
//  1. Unit terkunci (claim_locked_at / percobaan ≥ batas) → 423 claim_locked.
//  2. PIN salah → claim_failed_attempts + 1 (commit), terkunci di percobaan ke-N
//     + anomali CLAIM_PIN_BRUTE_FORCE. Brute force paralel tidak bisa melewati
//     batas karena setiap percobaan menunggu lock percobaan sebelumnya.
//  3. Klaim aktif sudah ada → 409 already_claimed (atau replay untuk akun sama).
//  4. Gerbang lifecycle: SOLD → poin langsung; AT_PARTNER → PENDING dengan masa
//     tunggu; CONSUMER_ACTIVATED tanpa klaim aktif (klaim lama ditolak) →
//     mengikuti status asal; status lain → 409 claim_not_allowed + anomali.
//  5. Skor risiko dari aturan CLAIM_* (bobot di params), di-clamp 0–100;
//     skor ≥ ambang → PENDING menunggu review.
//  6. Klaim + transisi serial → CONSUMER_ACTIVATED + ledger EARN
//     (idempotency_key) dalam transaksi yang sama.
func (s *LoyaltyService) Claim(ctx context.Context, p *auth.Principal, in ClaimInput) (ClaimResult, error) {
	if p == nil || p.Kind != auth.KindConsumer {
		return ClaimResult{}, apperror.ErrForbidden
	}
	code, okCode := domain.NormalizeSerialCode(in.Code)
	pin, okPIN := domain.NormalizeClaimPIN(in.PIN)
	if !okCode || !okPIN {
		return ClaimResult{}, ErrClaimInvalidCodeOrPIN
	}
	lc, err := s.serials.Lifecycle(ctx)
	if err != nil {
		return ClaimResult{}, err
	}
	now := s.clock.Now()
	consumerID := p.ID
	fp := s.ids.Fingerprint(in.AnonID)
	ipHash := s.ids.IPHash(in.ClientIP)
	var geo scanGeo
	if in.GPS != nil && in.GPS.Valid() {
		geo = scanGeo{Point: *in.GPS, Source: domain.GeoGPS, AccuracyM: in.AccuracyM}
	} else {
		geo = scanGeo{Source: domain.GeoNone}
	}

	var res ClaimResult
	// outcome: error yang dikembalikan SETELAH commit (percobaan PIN salah dan
	// anomali harus tetap tersimpan walau klaimnya ditolak).
	var outcome error
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := s.repo.ConsumerActive(tx, consumerID)
		if err != nil {
			return err
		}
		if !active {
			outcome = ErrClaimConsumerInactive
			return nil
		}
		unit, found, err := s.repo.LockUnitForClaim(tx, code)
		if err != nil {
			return err
		}
		if !found || len(unit.ClaimSecretHash) == 0 {
			outcome = ErrClaimInvalidCodeOrPIN
			return nil
		}
		rules, err := s.verify.Rules(tx, "CLAIM")
		if err != nil {
			return err
		}
		maxAttempts := defaultMaxPINAttempts
		if rule, ok := rules[domain.RuleClaimPINBruteForce]; ok {
			var rp domain.ClaimRiskParams
			if err := domain.ParseRuleParams(rule.Params, &rp); err != nil {
				return fmt.Errorf("params %s: %w", rule.Code, err)
			}
			if rp.MaxFailedAttempts > 0 {
				maxAttempts = rp.MaxFailedAttempts
			}
		}
		if unit.ClaimLockedAt != nil || unit.ClaimFailedAttempts >= maxAttempts {
			outcome = ErrClaimLocked
			return nil
		}
		if !domain.VerifyClaimPIN(s.pepper, unit.ID, pin, unit.ClaimSecretHash) {
			attempts, locked, err := s.repo.RecordFailedPIN(tx, unit.ID, maxAttempts, now)
			if err != nil {
				return err
			}
			outcome = ErrClaimInvalidCodeOrPIN
			if locked {
				outcome = ErrClaimLocked
				if err := s.claimAnomaly(tx, rules, domain.RuleClaimPINBruteForce, unit,
					"CLAIM_PIN_BRUTE_FORCE:"+unit.ID.String(), now,
					map[string]any{"serial_code": unit.Code, "attempts": attempts, "consumer_id": consumerID}); err != nil {
					return err
				}
			}
			return nil
		}
		if unit.ClaimFailedAttempts > 0 {
			if err := s.repo.ResetFailedPIN(tx, unit.ID); err != nil {
				return err
			}
		}

		existing, err := s.repo.ActiveClaim(tx, unit.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.ConsumerID == consumerID {
				res = ClaimResult{Claim: *existing, Replayed: true}
				return nil
			}
			outcome = ErrClaimAlreadyClaimed
			return nil
		}

		// Riwayat penolakan: akun yang klaimnya pada unit ini pernah ditolak/
		// dibalik tidak boleh mencoba lagi (cookie/perangkat baru tidak
		// menghapus riwayat ini), dan klaim siapa pun pada unit dengan riwayat
		// itu selalu menunggu review manusia.
		history, err := s.repo.RejectedHistory(tx, unit.ID, consumerID)
		if err != nil {
			return err
		}
		if history.ConsumerRejected {
			outcome = ErrClaimPreviouslyRejected
			return nil
		}

		origin := unit.Status
		if unit.Status == domain.SerialConsumerActivated {
			if origin, err = s.repo.ActivationOrigin(tx, unit.ID); err != nil {
				return err
			}
		}
		if origin != domain.SerialSold && origin != domain.SerialAtPartner {
			outcome = ErrClaimNotAllowed
			// Kode + PIN benar pada unit yang belum terjual: indikasi panen
			// poin oleh orang dalam (ancaman 4).
			return s.nonSellableClaimAnomaly(tx, unit, consumerID, now)
		}

		reasons, err := s.riskReasons(tx, rules, unit, consumerID, fp, geo, now)
		if err != nil {
			return err
		}
		total := 0
		for _, r := range reasons {
			total += r.Weight
		}
		score := domain.ClampRisk(total)
		review := score >= s.cfg.ClaimReviewThreshold
		if history.AnyRejected {
			review = true
			reasons = append(reasons, RiskReason{Rule: claimReasonPriorRejected, Weight: 0})
		}

		claimID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		points := int64(s.cfg.LoyaltyPointsPerUnit)
		if unit.ProductPoints != nil && *unit.ProductPoints >= 0 {
			points = *unit.ProductPoints
		}
		claim := model.LoyaltyClaim{
			ID: claimID, SerialUnitID: unit.ID, ConsumerID: consumerID, Status: model.ClaimApproved,
			Points: points, RiskScore: score, RiskReasons: model.MustJSON(reasonsOrEmpty(reasons)),
			AvailableAt: now, ReviewRequired: review,
		}
		if origin == domain.SerialAtPartner {
			claim.AvailableAt = now.Add(s.cfg.LoyaltyPendingPeriod)
		}
		if review || origin == domain.SerialAtPartner {
			claim.Status = model.ClaimPending
		}

		// Scan klaim ikut direkam (fingerprint untuk aturan perangkat bersama).
		var region *repository.RegionHit
		if geo.known() {
			if region, err = s.verify.ResolveRegion(tx, geo.Point, regionMaxKm); err != nil {
				return err
			}
		}
		scan, err := newScanEvent(code, ScanContextLoyaltyClaim, geo, region, fp, ipHash, now)
		if err != nil {
			return err
		}
		scan.SerialUnitID, scan.ConsumerID = &unit.ID, &consumerID
		scan.Result = domain.ResultGenuineRepeat
		switch {
		case unit.BatchStatus != nil && *unit.BatchStatus == "RECALLED":
			scan.Result = domain.ResultRecalled
		case unit.FirstVerifiedAt == nil:
			scan.Result = domain.ResultGenuineFirst
		}
		for _, r := range reasons {
			scan.RuleHits = append(scan.RuleHits, r.Rule)
		}
		if err := s.verify.InsertScan(tx, scan); err != nil {
			return err
		}
		claim.ScanEventID = &scan.ID

		if err := s.repo.InsertClaim(tx, &claim); err != nil {
			if mapped := mapSerialConstraint(err); errors.Is(mapped, ErrClaimAlreadyClaimed) {
				outcome = ErrClaimAlreadyClaimed
				return errClaimRollback
			}
			return fmt.Errorf("menyimpan klaim: %w", err)
		}

		consumer := SerialActor{Type: model.ActorConsumer, UserID: &consumerID}
		ref := EventRef{Type: repository.RefLoyaltyClaim, ID: &claimID, At: now}
		if unit.Status == domain.SerialConsumerActivated {
			// Klaim ulang setelah klaim lama ditolak: status tetap, event dicatat.
			evID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			if err := s.serials.repo.InsertEvent(tx, model.UnitEvent{
				ID: evID, SerialUnitID: unit.ID, BizStep: "consumer_activating",
				LocationID: unit.LocationID, PartyID: unit.CustodianPartyID,
				ActorType: consumer.Type, ActorID: consumer.UserID, RefType: serialPtr(ref.Type), RefID: ref.ID,
				Payload: model.MustJSON(map[string]any{"reclaim": true}), OccurredAt: now, RecordedAt: now,
			}); err != nil {
				return err
			}
		} else {
			if !lc.CanTransition(unit.Status, domain.SerialConsumerActivated) {
				outcome = ErrClaimNotAllowed
				return errClaimRollback
			}
			n, err := s.serials.bulkTransition(tx, lc, repository.SerialTransitionSpec{
				Roots: []uuid.UUID{unit.ID}, To: domain.SerialConsumerActivated,
			}, []string{unit.Status}, consumer, ref)
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrSerialConcurrentChange
			}
		}

		if points > 0 {
			ledgerID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			if _, err := s.repo.InsertLedger(tx, model.LoyaltyLedgerEntry{
				ID: ledgerID, ConsumerID: consumerID, EntryType: "EARN", Points: points,
				RefType: repository.RefLoyaltyClaim, RefID: claimID,
				IdempotencyKey: "claim:" + claimID.String() + ":earn", OccurredAt: now, RecordedAt: now,
			}); err != nil {
				return err
			}
		}
		if review {
			for _, r := range reasons {
				if err := s.claimAnomaly(tx, rules, r.Rule, unit, r.Rule+":"+consumerID.String(), now,
					map[string]any{"claim_id": claimID, "serial_code": unit.Code, "risk_score": score}); err != nil {
					return err
				}
			}
		}
		res = ClaimResult{Claim: claim, RiskReasons: reasons}
		return nil
	})
	if errors.Is(err, errClaimRollback) {
		return ClaimResult{}, outcome
	}
	if err != nil {
		return ClaimResult{}, err
	}
	if outcome != nil {
		return ClaimResult{}, outcome
	}
	return res, nil
}

// errClaimRollback membatalkan transaksi klaim; error asli ada di outcome.
var errClaimRollback = errors.New("klaim dibatalkan")

func reasonsOrEmpty(r []RiskReason) []RiskReason {
	if r == nil {
		return []RiskReason{}
	}
	return r
}

// nonSellableClaimAnomaly mencatat klaim (kode + PIN benar) pada unit yang
// belum terjual memakai aturan NON_SELLABLE_SCAN dengan konteks klaim.
func (s *LoyaltyService) nonSellableClaimAnomaly(tx *gorm.DB, unit repository.ClaimUnit, consumerID uuid.UUID, now time.Time) error {
	rules, err := s.verify.Rules(tx, "SCAN")
	if err != nil {
		return err
	}
	return s.claimAnomaly(tx, rules, domain.RuleNonSellableScan, unit, "NON_SELLABLE_SCAN:claim:"+unit.ID.String(), now,
		map[string]any{
			"context": ScanContextLoyaltyClaim, "serial_code": unit.Code,
			"status": unit.Status, "consumer_id": consumerID,
		})
}

// claimAnomaly meng-upsert anomali untuk aturan ruleCode (kalau enabled).
func (s *LoyaltyService) claimAnomaly(tx *gorm.DB, rules map[string]model.AnomalyRule, ruleCode string, unit repository.ClaimUnit, dedupe string, now time.Time, evidence map[string]any) error {
	rule, ok := rules[ruleCode]
	if !ok {
		return nil
	}
	subject := unit.ID
	return s.verify.UpsertAnomaly(tx, repository.RadarAnomalyHit{
		RuleCode: rule.Code, Severity: rule.Severity, SubjectType: "SERIAL_UNIT", SubjectID: &subject,
		PartyID: unit.CustodianPartyID, DedupeKey: dedupe, Evidence: evidence, At: now,
	})
}

// riskReasons mengevaluasi aturan CLAIM_* yang enabled. Bobot dan ambang
// dibaca dari params; jumlah bobot boleh di atas 100 (di-clamp pemanggil).
func (s *LoyaltyService) riskReasons(tx *gorm.DB, rules map[string]model.AnomalyRule, unit repository.ClaimUnit, consumerID uuid.UUID, fp []byte, geo scanGeo, now time.Time) ([]RiskReason, error) {
	var out []RiskReason
	params := func(code string) (domain.ClaimRiskParams, bool, error) {
		rule, ok := rules[code]
		if !ok {
			return domain.ClaimRiskParams{}, false, nil
		}
		p := domain.ClaimRiskParams{WindowHours: 24}
		if err := domain.ParseRuleParams(rule.Params, &p); err != nil {
			return p, false, fmt.Errorf("params %s: %w", code, err)
		}
		return p, true, nil
	}
	window := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }

	if p, ok, err := params(domain.RuleClaimNearStore); err != nil {
		return nil, err
	} else if ok && geo.known() && p.RadiusM > 0 {
		near, err := s.repo.NearOpenLocation(tx, geo.Point, p.RadiusM, now, p.DuringOpeningHours)
		if err != nil {
			return nil, err
		}
		if near {
			out = append(out, RiskReason{Rule: domain.RuleClaimNearStore, Weight: p.RiskWeight})
		}
	}
	if p, ok, err := params(domain.RuleClaimAccountVelocity); err != nil {
		return nil, err
	} else if ok && p.MaxClaims > 0 {
		n, err := s.repo.ClaimsByConsumerSince(tx, consumerID, window(p.WindowHours))
		if err != nil {
			return nil, err
		}
		if n+1 > int64(p.MaxClaims) {
			out = append(out, RiskReason{Rule: domain.RuleClaimAccountVelocity, Weight: p.RiskWeight})
		}
	}
	if p, ok, err := params(domain.RuleClaimCartonSpread); err != nil {
		return nil, err
	} else if ok && unit.ParentID != nil && p.MaxAccounts > 0 {
		n, err := s.repo.SiblingClaimAccounts(tx, *unit.ParentID, consumerID, window(p.WindowHours))
		if err != nil {
			return nil, err
		}
		if n+1 > int64(p.MaxAccounts) {
			out = append(out, RiskReason{Rule: domain.RuleClaimCartonSpread, Weight: p.RiskWeight})
		}
	}
	if p, ok, err := params(domain.RuleClaimSharedDevice); err != nil {
		return nil, err
	} else if ok && fp != nil && p.MaxAccounts > 0 {
		n, err := s.repo.FingerprintAccounts(tx, fp, consumerID)
		if err != nil {
			return nil, err
		}
		if n+1 > int64(p.MaxAccounts) {
			out = append(out, RiskReason{Rule: domain.RuleClaimSharedDevice, Weight: p.RiskWeight})
		}
	}
	return out, nil
}

// Review memutuskan klaim (permission loyalty.review):
//   - APPROVE / REJECT untuk klaim PENDING;
//   - REVERSE (clawback, note wajib) untuk klaim APPROVED.
//
// REJECT dan REVERSE membalik poin dengan ledger REVERSE (idempotency_key
// sama, jadi tidak pernah ganda), membebaskan unit (klaim REJECTED/REVERSED
// tidak dihitung one_active_claim_per_unit), dan, kalau tidak ada klaim aktif
// lain, mengembalikan serial CONSUMER_ACTIVATED ke status asalnya (SOLD /
// AT_PARTNER, dari event consumer_activating terakhir) lewat transisi
// claim_reversal, supaya unit kembali ikut recall, laporan jual mitra, dan
// retur. Lock: serial dulu, lalu klaim (urutan sama dengan Claim), sehingga
// keputusan yang bersaing untuk klaim yang sama diserialkan: satu menang,
// sisanya 409 claim_already_decided (atau idempotent kalau keputusannya sama).
func (s *LoyaltyService) Review(ctx context.Context, p *auth.Principal, claimID uuid.UUID, decision string, note *string) (model.LoyaltyClaim, error) {
	var target, from string
	switch decision {
	case ClaimDecisionApprove:
		target, from = model.ClaimApproved, model.ClaimPending
	case ClaimDecisionReject:
		target, from = model.ClaimRejected, model.ClaimPending
	case ClaimDecisionReverse:
		target, from = model.ClaimReversed, model.ClaimApproved
		if note == nil || strings.TrimSpace(*note) == "" {
			return model.LoyaltyClaim{}, ErrClaimReasonRequired
		}
	default:
		return model.LoyaltyClaim{}, apperror.Validation(apperror.FieldError{
			Field: "decision", Code: "oneof", Message: "harus salah satu dari: APPROVE REJECT REVERSE"})
	}
	lc, err := s.serials.Lifecycle(ctx)
	if err != nil {
		return model.LoyaltyClaim{}, err
	}
	actor := serialActorOf(p)
	now := s.clock.Now()
	var out model.LoyaltyClaim
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		peek, ok, err := s.repo.FindClaim(tx, claimID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrClaimNotFound
		}
		unitStatus, err := s.repo.LockSerialStatus(tx, peek.SerialUnitID)
		if err != nil {
			return err
		}
		claim, _, err := s.repo.LockClaim(tx, claimID)
		if err != nil {
			return err
		}
		if claim.Status != from {
			if claim.Status == target {
				out = claim
				return nil
			}
			return ErrClaimAlreadyDecided
		}
		if ok, err := s.repo.Decide(tx, claim.ID, from, target, actor.UserID, now); err != nil {
			return fmt.Errorf("memutuskan klaim: %w", err)
		} else if !ok {
			return ErrClaimAlreadyDecided
		}
		if target == model.ClaimRejected || target == model.ClaimReversed {
			if claim.Points > 0 {
				ledgerID, err := uuid.NewV7()
				if err != nil {
					return err
				}
				if _, err := s.repo.InsertLedger(tx, model.LoyaltyLedgerEntry{
					ID: ledgerID, ConsumerID: claim.ConsumerID, EntryType: "REVERSE", Points: -claim.Points,
					RefType: repository.RefLoyaltyClaim, RefID: claim.ID,
					IdempotencyKey: "claim:" + claim.ID.String() + ":reverse", OccurredAt: now, RecordedAt: now,
				}); err != nil {
					return err
				}
			}
			if err := s.restoreSerialAfterClaim(tx, lc, claim, unitStatus, actor, now); err != nil {
				return err
			}
		}
		auditID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if err := s.serials.repo.InsertAudit(tx, model.AuditLog{
			ID: auditID, ActorType: actor.Type, ActorID: actor.UserID, Action: "loyalty.claim.review",
			Entity: "loyalty_claims", EntityID: &claim.ID,
			Before: model.MustJSON(map[string]any{"status": claim.Status}),
			After:  model.MustJSON(map[string]any{"status": target}),
			Reason: note, At: now,
		}); err != nil {
			return err
		}
		claim.Status, claim.DecidedBy, claim.DecidedAt = target, actor.UserID, &now
		out = claim
		return nil
	})
	return out, err
}

// restoreSerialAfterClaim mengembalikan serial ke status sebelum aktivasi
// setelah klaimnya ditolak/dibalik. Kalau masih ada klaim aktif lain, atau
// status asal tidak diketahui, hanya event claim_reversal (tanpa perubahan
// status) yang dicatat.
func (s *LoyaltyService) restoreSerialAfterClaim(tx *gorm.DB, lc *domain.Lifecycle, claim model.LoyaltyClaim, unitStatus string, actor SerialActor, now time.Time) error {
	ref := EventRef{Type: repository.RefLoyaltyClaim, ID: &claim.ID, At: now, Payload: map[string]any{"claim_status": claim.Status}}
	other, err := s.repo.ActiveClaim(tx, claim.SerialUnitID)
	if err != nil {
		return err
	}
	origin := ""
	if other == nil && unitStatus == domain.SerialConsumerActivated {
		if origin, err = s.repo.ActivationOrigin(tx, claim.SerialUnitID); err != nil {
			return err
		}
	}
	if origin != "" && lc.CanTransition(domain.SerialConsumerActivated, origin) {
		n, err := s.serials.bulkTransition(tx, lc, repository.SerialTransitionSpec{
			Roots: []uuid.UUID{claim.SerialUnitID}, To: origin,
		}, []string{domain.SerialConsumerActivated}, actor, ref)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrSerialConcurrentChange
		}
		return nil
	}
	evID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	return s.serials.repo.InsertEvent(tx, model.UnitEvent{
		ID: evID, SerialUnitID: claim.SerialUnitID, BizStep: "claim_reversal",
		ActorType: actor.Type, ActorID: actor.UserID, RefType: serialPtr(repository.RefLoyaltyClaim), RefID: &claim.ID,
		Payload: model.MustJSON(map[string]any{"status_restored": false}), OccurredAt: now, RecordedAt: now,
	})
}

// ReleaseDueClaims meng-approve klaim PENDING yang masa tunggunya habis dan
// tidak butuh review. Dipanggil worker berkala; satu statement, idempotent.
func (s *LoyaltyService) ReleaseDueClaims(ctx context.Context) (int64, error) {
	return s.repo.ReleaseDue(s.db.WithContext(ctx), s.clock.Now())
}

// Balance mengembalikan saldo poin konsumen (tersedia vs masih tertahan).
func (s *LoyaltyService) Balance(ctx context.Context, p *auth.Principal) (repository.LoyaltyBalance, error) {
	if p == nil || p.Kind != auth.KindConsumer {
		return repository.LoyaltyBalance{}, apperror.ErrForbidden
	}
	return s.repo.Balance(s.db.WithContext(ctx), p.ID, s.clock.Now())
}
